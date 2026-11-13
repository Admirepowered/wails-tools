package miproxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// VMess wire constants (ported from the C implementation).
const (
	vmessVersion   = 0x01
	vmessOptChunk  = 0x01
	vmessCmdTCP    = 0x01
	vmessSecNone   = 0x05
	vmessSecAESGCM = 0x03
	vmessSecChaCha = 0x04
	vmessTagSize   = 16
	vmessMaxChunk  = 4096
	vmessKDFSalt   = "VMess AEAD KDF"
	vmessUUIDMagic = "c48619fe-8f02-49e0-b9e9-edf763e17e21"
)

type bodySecurity int

const (
	bodyNone bodySecurity = iota
	bodyAESGCM
	bodyChaCha
)

func parseBodySecurity(name string) bodySecurity {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto", "aes-128-gcm", "aes128-gcm":
		return bodyAESGCM
	case "chacha20-poly1305", "chacha20-ietf-poly1305", "chacha20":
		return bodyChaCha
	case "none", "zero":
		return bodyNone
	}
	return bodyAESGCM
}

func (s bodySecurity) byteValue() byte {
	switch s {
	case bodyAESGCM:
		return vmessSecAESGCM
	case bodyChaCha:
		return vmessSecChaCha
	default:
		return vmessSecNone
	}
}

// kdfNode is one level of the nested-HMAC KDF chain.
type kdfNode struct {
	key    []byte
	parent *kdfNode
}

// kdfCompute mirrors the reference implementation: the root level is a plain
// HMAC with the salt, and every path level applies ipad/opad with the path
// value as key before delegating to its parent.
func kdfCompute(node *kdfNode, data []byte) []byte {
	if node.parent == nil {
		mac := hmac.New(sha256.New, node.key)
		mac.Write(data)
		return mac.Sum(nil)
	}
	var block [64]byte
	if len(node.key) > 64 {
		copy(block[:], kdfCompute(node.parent, node.key))
	} else {
		copy(block[:], node.key)
	}
	inner := make([]byte, 64+len(data))
	for i := 0; i < 64; i++ {
		inner[i] = block[i] ^ 0x36
	}
	copy(inner[64:], data)
	innerHash := kdfCompute(node.parent, inner)
	outer := make([]byte, 64+len(innerHash))
	for i := 0; i < 64; i++ {
		outer[i] = block[i] ^ 0x5c
	}
	copy(outer[64:], innerHash)
	return kdfCompute(node.parent, outer)
}

// vmessKDF derives a key with the VMess AEAD KDF chain.
func vmessKDF(key []byte, paths ...string) []byte {
	cur := &kdfNode{key: []byte(vmessKDFSalt)}
	for _, p := range paths {
		cur = &kdfNode{key: []byte(p), parent: cur}
	}
	return kdfCompute(cur, key)
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// uuidBytes parses the canonical 8-4-4-4-12 UUID form.
func uuidBytes(s string) ([]byte, error) {
	clean := strings.ReplaceAll(strings.TrimSpace(s), "-", "")
	if len(clean) != 32 {
		return nil, fmt.Errorf("invalid uuid %q", s)
	}
	out := make([]byte, 16)
	for i := 0; i < 16; i++ {
		var v byte
		for j := 0; j < 2; j++ {
			c := clean[i*2+j]
			var nib byte
			switch {
			case c >= '0' && c <= '9':
				nib = c - '0'
			case c >= 'a' && c <= 'f':
				nib = c - 'a' + 10
			case c >= 'A' && c <= 'F':
				nib = c - 'A' + 10
			default:
				return nil, fmt.Errorf("invalid uuid %q", s)
			}
			v = v<<4 | nib
		}
		out[i] = v
	}
	return out, nil
}

func fnv1a32(data []byte) uint32 {
	h := fnv.New32a()
	_, _ = h.Write(data)
	return h.Sum32()
}

// vmessConn is a stream speaking the VMess protocol.
type vmessConn struct {
	conn net.Conn

	reqKey   [16]byte
	reqIV    [16]byte
	respKey  [16]byte
	respIV   [16]byte
	respV    byte
	reqCount uint32
	respCnt  uint32
	security bodySecurity
	aead     bool

	writeMu sync.Mutex
	readMu  sync.Mutex
	readBuf []byte
	respHdr bool
}

func newVMessConn(node Node, conn net.Conn, target string) (*vmessConn, error) {
	v := &vmessConn{conn: conn, security: parseBodySecurity(node.Security), aead: node.AlterID == 0}
	if _, err := rand.Read(v.reqKey[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(v.reqIV[:]); err != nil {
		return nil, err
	}
	var vBuf [1]byte
	if _, err := rand.Read(vBuf[:]); err == nil {
		v.respV = vBuf[0]
	} else {
		v.respV = 0x2a
	}
	if v.aead {
		sum := sha256.Sum256(v.reqKey[:])
		copy(v.respKey[:], sum[:16])
		sum = sha256.Sum256(v.reqIV[:])
		copy(v.respIV[:], sum[:16])
	} else {
		k := md5.Sum(v.reqKey[:])
		copy(v.respKey[:], k[:])
		iv := md5.Sum(v.reqIV[:])
		copy(v.respIV[:], iv[:])
	}

	uuid, err := uuidBytes(node.UUID)
	if err != nil {
		return nil, err
	}
	cmdKey := md5.Sum(append(append([]byte{}, uuid...), []byte(vmessUUIDMagic)...))

	plain, err := v.buildHeader(cmdKey[:], node, target)
	if err != nil {
		return nil, err
	}
	if v.aead {
		sealed, err := v.sealAEADHeader(cmdKey[:], plain)
		if err != nil {
			return nil, err
		}
		if _, err := conn.Write(sealed); err != nil {
			return nil, err
		}
	} else {
		legacy, err := v.buildLegacyHeader(cmdKey[:], plain, uuid)
		if err != nil {
			return nil, err
		}
		if _, err := conn.Write(legacy); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func (v *vmessConn) buildHeader(cmdKey []byte, node Node, target string) ([]byte, error) {
	paddingLen := 0
	var rnd [1]byte
	if _, err := rand.Read(rnd[:]); err == nil {
		paddingLen = int(rnd[0] & 0x0F)
	}
	buf := make([]byte, 0, 512)
	buf = append(buf, vmessVersion)
	buf = append(buf, v.reqIV[:]...)
	buf = append(buf, v.reqKey[:]...)
	buf = append(buf, v.respV)
	buf = append(buf, vmessOptChunk)
	buf = append(buf, byte(paddingLen<<4)|v.security.byteValue())
	buf = append(buf, 0x00)
	buf = append(buf, vmessCmdTCP)
	dest, err := encodeDestination(target)
	if err != nil {
		return nil, err
	}
	buf = append(buf, dest...)
	if paddingLen > 0 {
		pad := make([]byte, paddingLen)
		_, _ = rand.Read(pad)
		buf = append(buf, pad...)
	}
	checksum := fnv1a32(buf)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], checksum)
	return append(buf, sum[:]...), nil
}

func encodeDestination(target string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("bad target %q: %w", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("bad port %q", portStr)
	}
	out := make([]byte, 0, 32)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	out = append(out, p[:]...)
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, 0x01)
			out = append(out, v4...)
			return out, nil
		}
		out = append(out, 0x03)
		out = append(out, ip.To16()...)
		return out, nil
	}
	if len(host) > 255 {
		return nil, errors.New("domain too long")
	}
	out = append(out, 0x02, byte(len(host)))
	out = append(out, []byte(host)...)
	return out, nil
}

// sealAEADHeader builds authID + encrypted length + nonce + encrypted payload.
func (v *vmessConn) sealAEADHeader(cmdKey, payload []byte) ([]byte, error) {
	authID, err := createAuthID(cmdKey)
	if err != nil {
		return nil, err
	}
	var connNonce [8]byte
	if _, err := rand.Read(connNonce[:]); err != nil {
		return nil, err
	}

	lengthKey := vmessKDF(cmdKey, "VMess Header AEAD Key_Length", string(authID), string(connNonce[:]))
	lengthNonce := vmessKDF(cmdKey, "VMess Header AEAD Nonce_Length", string(authID), string(connNonce[:]))
	payloadKey := vmessKDF(cmdKey, "VMess Header AEAD Key", string(authID), string(connNonce[:]))
	payloadNonce := vmessKDF(cmdKey, "VMess Header AEAD Nonce", string(authID), string(connNonce[:]))

	var lenPlain [2]byte
	binary.BigEndian.PutUint16(lenPlain[:], uint16(len(payload)))
	lenCipher, err := sealAESGCM(lengthKey[:16], lengthNonce[:12], lenPlain[:], authID)
	if err != nil {
		return nil, err
	}
	payloadCipher, err := sealAESGCM(payloadKey[:16], payloadNonce[:12], payload, authID)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 16+len(lenCipher)+8+len(payloadCipher))
	out = append(out, authID...)
	out = append(out, lenCipher...)
	out = append(out, connNonce[:]...)
	out = append(out, payloadCipher...)
	return out, nil
}

// createAuthID reproduces the timestamp/CRC/AES-ECB auth id construction.
func createAuthID(cmdKey []byte) ([]byte, error) {
	var input [16]byte
	binary.BigEndian.PutUint64(input[0:], uint64(time.Now().Unix()))
	if _, err := rand.Read(input[8:12]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(input[12:], crc32.ChecksumIEEE(input[0:12]))

	kdfOut := vmessKDF(cmdKey, "AES Auth ID Encryption")
	block, err := aes.NewCipher(kdfOut[:16])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	block.Encrypt(out, input[:])
	return out, nil
}

// buildLegacyHeader produces the pre-AEAD header (alterId > 0).
func (v *vmessConn) buildLegacyHeader(cmdKey, plain []byte, uuid []byte) ([]byte, error) {
	ts := uint64(time.Now().Unix())
	var msg [32]byte
	var tsBytes [8]byte
	binary.BigEndian.PutUint64(tsBytes[:], ts)
	for i := 0; i < 4; i++ {
		copy(msg[i*8:], tsBytes[:])
	}
	// auth = HMAC-MD5(uuid, ts||ts||ts||ts)
	mac := hmac.New(md5.New, uuid)
	mac.Write(msg[:])
	var auth [16]byte
	copy(auth[:], mac.Sum(nil))
	// iv = MD5(ts||ts||ts||ts)
	ivSum := md5.Sum(msg[:])

	block, err := aes.NewCipher(cmdKey)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16+len(plain))
	copy(out, auth[:])
	stream := cipher.NewCFBEncrypter(block, ivSum[:])
	stream.XORKeyStream(out[16:], plain)
	return out, nil
}

func sealAESGCM(key, nonce, plain, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plain, aad), nil
}

func openAESGCM(key, nonce, cipherText, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, cipherText, aad)
}

// chunkNonce derives the per-chunk nonce from the IV and counter.
func chunkNonce(iv [16]byte, count uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, iv[:12])
	nonce[0] = byte(count >> 8)
	nonce[1] = byte(count)
	return nonce
}

func chachaKeyFrom(key [16]byte) []byte {
	first := md5.Sum(key[:])
	second := md5.Sum(first[:])
	out := make([]byte, 32)
	copy(out[:16], first[:])
	copy(out[16:], second[:])
	return out
}

// Write sends data as encrypted VMess chunks.
func (v *vmessConn) Write(p []byte) (int, error) {
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > vmessMaxChunk {
			chunk = chunk[:vmessMaxChunk]
		}
		frame, err := v.encryptChunk(chunk)
		if err != nil {
			return total, err
		}
		if _, err := v.conn.Write(frame); err != nil {
			return total, err
		}
		total += len(chunk)
		p = p[len(chunk):]
	}
	return total, nil
}

func (v *vmessConn) encryptChunk(plain []byte) ([]byte, error) {
	if v.security == bodyNone {
		frame := make([]byte, 2+len(plain))
		binary.BigEndian.PutUint16(frame, uint16(len(plain)))
		copy(frame[2:], plain)
		return frame, nil
	}
	nonce := chunkNonce(v.reqIV, v.reqCount)
	v.reqCount++
	var sealed []byte
	var err error
	switch v.security {
	case bodyAESGCM:
		sealed, err = sealAESGCM(v.reqKey[:], nonce, plain, nil)
	case bodyChaCha:
		var aead cipher.AEAD
		aead, err = chacha20poly1305.New(chachaKeyFrom(v.reqKey))
		if err == nil {
			sealed = aead.Seal(nil, nonce, plain, nil)
		}
	}
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 2+len(sealed))
	binary.BigEndian.PutUint16(frame, uint16(len(sealed)))
	copy(frame[2:], sealed)
	return frame, nil
}

// Read decrypts VMess chunks into p.
func (v *vmessConn) Read(p []byte) (int, error) {
	v.readMu.Lock()
	defer v.readMu.Unlock()
	for len(v.readBuf) == 0 {
		if !v.respHdr {
			if err := v.readResponseHeader(); err != nil {
				return 0, err
			}
			v.respHdr = true
			continue
		}
		if err := v.readChunk(); err != nil {
			return 0, err
		}
	}
	n := copy(p, v.readBuf)
	v.readBuf = v.readBuf[n:]
	return n, nil
}

func (v *vmessConn) readResponseHeader() error {
	if v.aead {
		encLen := make([]byte, 2+vmessTagSize)
		if _, err := io.ReadFull(v.conn, encLen); err != nil {
			return err
		}
		lenKey := vmessKDF(v.respKey[:], "AEAD Resp Header Len Key")
		lenNonce := vmessKDF(v.respIV[:], "AEAD Resp Header Len IV")
		plainLen, err := openAESGCM(lenKey[:16], lenNonce[:12], encLen, nil)
		if err != nil || len(plainLen) != 2 {
			return errors.New("vmess: bad response header length")
		}
		length := int(binary.BigEndian.Uint16(plainLen))
		if length <= 0 || length > 4096 {
			return errors.New("vmess: invalid response header length")
		}
		encHdr := make([]byte, length+vmessTagSize)
		if _, err := io.ReadFull(v.conn, encHdr); err != nil {
			return err
		}
		hdrKey := vmessKDF(v.respKey[:], "AEAD Resp Header Key")
		hdrNonce := vmessKDF(v.respIV[:], "AEAD Resp Header IV")
		hdr, err := openAESGCM(hdrKey[:16], hdrNonce[:12], encHdr, nil)
		if err != nil || len(hdr) != length {
			return errors.New("vmess: bad response header")
		}
		if len(hdr) < 4 || hdr[0] != v.respV || hdr[2] != 0x00 {
			return errors.New("vmess: response header mismatch")
		}
		return nil
	}

	// Legacy: 4-byte AES-CFB response header.
	enc := make([]byte, 4)
	if _, err := io.ReadFull(v.conn, enc); err != nil {
		return err
	}
	block, err := aes.NewCipher(v.respKey[:])
	if err != nil {
		return err
	}
	dec := make([]byte, 4)
	cipher.NewCFBDecrypter(block, v.respIV[:]).XORKeyStream(dec, enc)
	if dec[0] != v.respV {
		return errors.New("vmess: legacy response header mismatch")
	}
	if dec[2] != 0 && dec[3] > 0 {
		extra := make([]byte, dec[3])
		if _, err := io.ReadFull(v.conn, extra); err != nil {
			return err
		}
		cipher.NewCFBDecrypter(block, v.respIV[:]).XORKeyStream(extra, extra)
	}
	return nil
}

func (v *vmessConn) readChunk() error {
	var sizeBuf [2]byte
	if _, err := io.ReadFull(v.conn, sizeBuf[:]); err != nil {
		return err
	}
	size := int(binary.BigEndian.Uint16(sizeBuf[:]))
	if size == 0 {
		return nil
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(v.conn, payload); err != nil {
		return err
	}
	if v.security == bodyNone {
		v.readBuf = payload
		return nil
	}
	nonce := chunkNonce(v.respIV, v.respCnt)
	v.respCnt++
	var plain []byte
	var err error
	switch v.security {
	case bodyAESGCM:
		plain, err = openAESGCM(v.respKey[:], nonce, payload, nil)
	case bodyChaCha:
		var aead cipher.AEAD
		aead, err = chacha20poly1305.New(chachaKeyFrom(v.respKey))
		if err == nil {
			plain, err = aead.Open(nil, nonce, payload, nil)
		}
	}
	if err != nil {
		return err
	}
	v.readBuf = plain
	return nil
}

func (v *vmessConn) Close() error                       { return v.conn.Close() }
func (v *vmessConn) LocalAddr() net.Addr                { return v.conn.LocalAddr() }
func (v *vmessConn) RemoteAddr() net.Addr               { return v.conn.RemoteAddr() }
func (v *vmessConn) SetDeadline(t time.Time) error      { return v.conn.SetDeadline(t) }
func (v *vmessConn) SetReadDeadline(t time.Time) error  { return v.conn.SetReadDeadline(t) }
func (v *vmessConn) SetWriteDeadline(t time.Time) error { return v.conn.SetWriteDeadline(t) }
