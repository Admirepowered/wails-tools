package netdisk

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"wailstools/internal/applog"
)

const (
	baiduUploadChunkSize = 4 * 1024 * 1024
	quarkUploadChunkSize = 8 * 1024 * 1024
	quarkOSSUserAgent    = "aliyun-sdk-js/6.18.0 Chrome 112.0.0.0 on Windows 10 64-bit"
)

type uploadProgressFunc func(uploaded int64)

func (c *BaiduClient) UploadFile(ctx context.Context, account Account, cookie string, req UploadFileRequest, progress func(uploaded int64)) (UploadResult, error) {
	localPath, info, filename, err := normalizeUploadRequest(req)
	if err != nil {
		return UploadResult{}, err
	}
	targetPath := buildChildPath(firstNonEmpty(req.TargetPath, "/"), filename)
	done := applog.Begin("netdisk.baidu", "upload_file", map[string]any{
		"account_id": account.ID,
		"local_path": localPath,
		"target":     targetPath,
		"size":       info.Size(),
	})

	blocks, contentMD5, sliceMD5, err := baiduFileBlockMD5s(localPath, baiduUploadChunkSize)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	uploadID, rapid, err := c.baiduPrecreate(ctx, cookie, targetPath, info.Size(), blocks, contentMD5, sliceMD5)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	if rapid {
		if progress != nil {
			progress(info.Size())
		}
		result := UploadResult{Name: filename, Path: targetPath, Size: info.Size()}
		done(nil, map[string]any{"rapid": true, "path": targetPath})
		return result, nil
	}
	if uploadID == "" {
		err := errors.New("baidu upload id is missing")
		done(err, nil)
		return UploadResult{}, err
	}
	if err := c.baiduUploadParts(ctx, cookie, localPath, filename, targetPath, uploadID, info.Size(), int64(baiduUploadChunkSize), progress); err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	result, err := c.baiduCreateUploadedFile(ctx, cookie, targetPath, uploadID, info.Size(), blocks, req.Overwrite)
	done(err, map[string]any{"path": result.Path, "file_id": result.FileID, "size": result.Size})
	return result, err
}

func baiduFileBlockMD5s(filePath string, chunkSize int64) ([]string, string, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, "", "", err
	}
	defer file.Close()
	fullHash := md5.New()
	buf := make([]byte, chunkSize)
	blocks := make([]string, 0)
	first := ""
	for {
		n, readErr := io.ReadFull(file, buf)
		if readErr == io.EOF {
			break
		}
		if readErr == io.ErrUnexpectedEOF {
			readErr = nil
		}
		if n > 0 {
			chunk := buf[:n]
			_, _ = fullHash.Write(chunk)
			sum := md5.Sum(chunk)
			partMD5 := hex.EncodeToString(sum[:])
			if first == "" {
				first = partMD5
			}
			blocks = append(blocks, partMD5)
		}
		if readErr != nil {
			return nil, "", "", readErr
		}
		if n < len(buf) {
			break
		}
	}
	if len(blocks) == 0 {
		sum := md5.Sum(nil)
		first = hex.EncodeToString(sum[:])
		blocks = append(blocks, first)
	}
	return blocks, hex.EncodeToString(fullHash.Sum(nil)), first, nil
}

func (c *BaiduClient) baiduPrecreate(ctx context.Context, cookie, targetPath string, size int64, blocks []string, contentMD5, sliceMD5 string) (string, bool, error) {
	blockList, err := json.Marshal(blocks)
	if err != nil {
		return "", false, err
	}
	values := url.Values{}
	values.Set("path", targetPath)
	values.Set("size", strconv.FormatInt(size, 10))
	values.Set("isdir", "0")
	values.Set("autoinit", "1")
	values.Set("rtype", "1")
	values.Set("block_list", string(blockList))
	values.Set("content-md5", contentMD5)
	values.Set("slice-md5", sliceMD5)
	values.Set("local_mtime", strconv.FormatInt(time.Now().Unix(), 10))
	endpoint := "https://pan.baidu.com/api/precreate?clienttype=0&app_id=250528&web=1"
	var out struct {
		Errno      int    `json:"errno"`
		Error      string `json:"error_msg"`
		Errmsg     string `json:"errmsg"`
		ShowMsg    string `json:"show_msg"`
		UploadID   string `json:"uploadid"`
		ReturnType int    `json:"return_type"`
	}
	if err := c.postFormJSON(ctx, endpoint, cookie, values, &out); err != nil {
		return "", false, err
	}
	if out.Errno != 0 {
		return "", false, baiduAPIError("upload precreate", out.Errno, out.Error, out.Errmsg, out.ShowMsg, strconv.Itoa(out.Errno))
	}
	return out.UploadID, out.ReturnType == 2, nil
}

func (c *BaiduClient) baiduUploadParts(ctx context.Context, cookie, localPath, filename, targetPath, uploadID string, size, chunkSize int64, progress uploadProgressFunc) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	var uploaded int64
	partCount := int((size + chunkSize - 1) / chunkSize)
	if partCount <= 0 {
		partCount = 1
	}
	for part := 0; part < partCount; part++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		offset := int64(part) * chunkSize
		length := chunkSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		if length < 0 {
			length = 0
		}
		if err := c.baiduUploadPart(ctx, cookie, file, filename, targetPath, uploadID, part, offset, length); err != nil {
			return err
		}
		uploaded += length
		if progress != nil {
			progress(uploaded)
		}
	}
	return nil
}

func (c *BaiduClient) baiduUploadPart(ctx context.Context, cookie string, file *os.File, filename, targetPath, uploadID string, part int, offset, length int64) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	partWriter, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(partWriter, io.NewSectionReader(file, offset, length), length); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	values := url.Values{}
	values.Set("method", "upload")
	values.Set("type", "tmpfile")
	values.Set("app_id", "250528")
	values.Set("path", targetPath)
	values.Set("uploadid", uploadID)
	values.Set("partseq", strconv.Itoa(part))
	endpoint := "https://d.pcs.baidu.com/rest/2.0/pcs/superfile2?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Referer", "https://pan.baidu.com/")
	req.Header.Set("Cookie", cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ErrProviderUnauthorized
		}
		return fmt.Errorf("baidu upload part failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		Errno int    `json:"errno"`
		Error string `json:"error_msg"`
	}
	if len(bytes.TrimSpace(data)) > 0 && json.Unmarshal(data, &out) == nil && out.Errno != 0 {
		return baiduAPIError("upload part", out.Errno, out.Error, strconv.Itoa(out.Errno))
	}
	return nil
}

func (c *BaiduClient) baiduCreateUploadedFile(ctx context.Context, cookie, targetPath, uploadID string, size int64, blocks []string, overwrite bool) (UploadResult, error) {
	blockList, err := json.Marshal(blocks)
	if err != nil {
		return UploadResult{}, err
	}
	values := url.Values{}
	values.Set("path", targetPath)
	values.Set("size", strconv.FormatInt(size, 10))
	values.Set("isdir", "0")
	values.Set("uploadid", uploadID)
	values.Set("block_list", string(blockList))
	values.Set("rtype", "1")
	if overwrite {
		values.Set("rtype", "3")
	}
	values.Set("local_mtime", strconv.FormatInt(time.Now().Unix(), 10))
	endpoint := "https://pan.baidu.com/api/create?clienttype=0&app_id=250528&web=1"
	var out struct {
		Errno  int    `json:"errno"`
		Error  string `json:"error_msg"`
		Errmsg string `json:"errmsg"`
		Data   struct {
			FSID           any    `json:"fs_id"`
			Path           string `json:"path"`
			ServerFilename string `json:"server_filename"`
			Size           int64  `json:"size"`
		} `json:"data"`
		FSID any    `json:"fs_id"`
		Path string `json:"path"`
		Name string `json:"server_filename"`
		Size int64  `json:"size"`
	}
	if err := c.postFormJSON(ctx, endpoint, cookie, values, &out); err != nil {
		return UploadResult{}, err
	}
	if out.Errno != 0 {
		return UploadResult{}, baiduAPIError("create uploaded file", out.Errno, out.Error, out.Errmsg, strconv.Itoa(out.Errno))
	}
	return UploadResult{
		FileID: stringifyID(firstNonNil(out.Data.FSID, out.FSID)),
		Name:   firstNonEmpty(out.Data.ServerFilename, out.Name, netdiskBaseName(targetPath)),
		Path:   firstNonEmpty(out.Data.Path, out.Path, targetPath),
		Size:   firstPositiveInt64Local(out.Data.Size, out.Size, size),
	}, nil
}

func (c *QuarkClient) UploadFile(ctx context.Context, account Account, cookie string, req UploadFileRequest, progress func(uploaded int64)) (UploadResult, error) {
	localPath, info, filename, err := normalizeUploadRequest(req)
	if err != nil {
		return UploadResult{}, err
	}
	parentID := strings.TrimSpace(req.TargetParentID)
	if parentID == "" {
		parentID = "0"
	}
	done := applog.Begin("netdisk.quark", "upload_file", map[string]any{
		"account_id": account.ID,
		"local_path": localPath,
		"parent_id":  parentID,
		"filename":   filename,
		"size":       info.Size(),
	})

	fileMD5, fileSHA1, err := quarkFileHashes(localPath)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	pre, err := c.quarkUploadPre(ctx, cookie, parentID, filename, localPath, info)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	if pre.Data.Finish {
		if progress != nil {
			progress(info.Size())
		}
		result := UploadResult{FileID: pre.Data.FID, Name: filename, Size: info.Size()}
		done(nil, map[string]any{"rapid": true, "file_id": result.FileID})
		return result, nil
	}
	hashResult, hashFinished, err := c.quarkUploadHash(ctx, cookie, pre.Data.TaskID, filename, info.Size(), fileMD5, fileSHA1)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	if hashFinished {
		if progress != nil {
			progress(info.Size())
		}
		done(nil, map[string]any{"rapid": true, "file_id": hashResult.FileID, "stage": "hash"})
		return hashResult, nil
	}
	chunkSize := pre.PartSize()
	if chunkSize <= 0 {
		chunkSize = quarkUploadChunkSize
	}
	parts, err := c.quarkUploadParts(ctx, cookie, localPath, filename, info.Size(), chunkSize, pre, progress)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	if err := c.quarkCompleteOSSUpload(ctx, cookie, pre, parts); err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	result, err := c.quarkUploadFinish(ctx, cookie, pre.Data.TaskID, pre.Data.ObjKey, filename, info.Size())
	done(err, map[string]any{"file_id": result.FileID, "filename": result.Name, "size": result.Size})
	return result, err
}

type quarkLikeUploadClient interface {
	uploadHTTPClient() *http.Client
	uploadPostJSON(ctx context.Context, endpoint, cookie string, body any, out any) error
	uploadEndpoint(action string) string
	uploadReferer() string
	uploadComponent() string
}

func (c *UCClient) UploadFile(ctx context.Context, account Account, cookie string, req UploadFileRequest, progress func(uploaded int64)) (UploadResult, error) {
	return quarkLikeUploadFile(ctx, c, account, cookie, req, progress)
}

func (c *UCClient) uploadHTTPClient() *http.Client {
	if c.http != nil {
		return c.http
	}
	return http.DefaultClient
}

func (c *UCClient) uploadPostJSON(ctx context.Context, endpoint, cookie string, body any, out any) error {
	return c.postJSON(ctx, endpoint, cookie, body, out)
}

func (c *UCClient) uploadEndpoint(action string) string {
	return ucQuarkEndpoint("/1/clouddrive/file/"+strings.TrimLeft(action, "/"), nil)
}

func (c *UCClient) uploadReferer() string {
	return "https://drive.uc.cn/"
}

func (c *UCClient) uploadComponent() string {
	return "netdisk.uc"
}

func quarkLikeUploadFile(ctx context.Context, api quarkLikeUploadClient, account Account, cookie string, req UploadFileRequest, progress func(uploaded int64)) (UploadResult, error) {
	localPath, info, filename, err := normalizeUploadRequest(req)
	if err != nil {
		return UploadResult{}, err
	}
	parentID := strings.TrimSpace(req.TargetParentID)
	if parentID == "" {
		parentID = "0"
	}
	done := applog.Begin(api.uploadComponent(), "upload_file", map[string]any{
		"account_id": account.ID,
		"local_path": localPath,
		"parent_id":  parentID,
		"filename":   filename,
		"size":       info.Size(),
	})

	fileMD5, fileSHA1, err := quarkFileHashes(localPath)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	pre, err := quarkLikeUploadPre(ctx, api, cookie, parentID, filename, info)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	if pre.Data.Finish {
		if progress != nil {
			progress(info.Size())
		}
		result := UploadResult{FileID: pre.Data.FID, Name: filename, Size: info.Size()}
		done(nil, map[string]any{"rapid": true, "file_id": result.FileID})
		return result, nil
	}
	hashResult, hashFinished, err := quarkLikeUploadHash(ctx, api, cookie, pre.Data.TaskID, filename, info.Size(), fileMD5, fileSHA1)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	if hashFinished {
		if progress != nil {
			progress(info.Size())
		}
		done(nil, map[string]any{"rapid": true, "file_id": hashResult.FileID, "stage": "hash"})
		return hashResult, nil
	}
	chunkSize := pre.PartSize()
	if chunkSize <= 0 {
		chunkSize = quarkUploadChunkSize
	}
	parts, err := quarkLikeUploadParts(ctx, api, cookie, localPath, filename, info.Size(), chunkSize, pre, progress)
	if err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	if err := quarkLikeCompleteOSSUpload(ctx, api, cookie, pre, parts); err != nil {
		done(err, nil)
		return UploadResult{}, err
	}
	result, err := quarkLikeUploadFinish(ctx, api, cookie, pre.Data.TaskID, pre.Data.ObjKey, filename, info.Size())
	done(err, map[string]any{"file_id": result.FileID, "filename": result.Name, "size": result.Size})
	return result, err
}

func quarkLikeUploadPre(ctx context.Context, api quarkLikeUploadClient, cookie, parentID, filename string, info os.FileInfo) (quarkUploadPreResponse, error) {
	now := time.Now().UnixMilli()
	mod := info.ModTime().UnixMilli()
	if mod <= 0 {
		mod = now
	}
	body := map[string]any{
		"ccp_hash_update": true,
		"dir_name":        "",
		"file_name":       filename,
		"format_type":     strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), "."),
		"l_created_at":    mod,
		"l_updated_at":    mod,
		"pdir_fid":        parentID,
		"size":            info.Size(),
		"source":          "desktop",
		"parallel_upload": true,
	}
	var out quarkUploadPreResponse
	if err := api.uploadPostJSON(ctx, api.uploadEndpoint("upload/pre"), cookie, body, &out); err != nil {
		return out, err
	}
	if out.Status != 0 && out.Status != 200 {
		if err := out.LimitError(api.uploadComponent()); err != nil {
			return out, err
		}
		if api.uploadComponent() == "netdisk.uc" {
			return out, ucOperationError("upload_pre", out.Message, out.Status, out.Code)
		}
		return out, fmt.Errorf("%s upload pre failed: %s", api.uploadComponent(), firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	if strings.TrimSpace(out.Data.TaskID) == "" && !out.Data.Finish {
		if api.uploadComponent() == "netdisk.uc" {
			return out, errors.New("上传 UC 网盘文件失败：上游未返回上传任务 ID")
		}
		return out, fmt.Errorf("%s upload task id is missing", api.uploadComponent())
	}
	applog.Info(api.uploadComponent(), "upload_pre_ready", map[string]any{
		"task_id":       out.Data.TaskID != "",
		"upload_id":     out.Data.UploadID != "",
		"bucket":        out.Data.Bucket != "",
		"region":        out.Data.Region != "",
		"obj_key":       out.Data.ObjKey != "",
		"callback":      out.Data.Callback.HeaderValue() != "",
		"finish":        out.Data.Finish,
		"part_size":     out.PartSize(),
		"has_auth_info": strings.TrimSpace(out.Data.AuthInfo) != "",
	})
	return out, nil
}

func quarkLikeUploadHash(ctx context.Context, api quarkLikeUploadClient, cookie, taskID, filename string, size int64, fileMD5, fileSHA1 string) (UploadResult, bool, error) {
	if strings.TrimSpace(taskID) == "" {
		if api.uploadComponent() == "netdisk.uc" {
			return UploadResult{}, false, errors.New("校验 UC 网盘文件失败：上传任务 ID 不能为空")
		}
		return UploadResult{}, false, fmt.Errorf("%s upload task id is required", api.uploadComponent())
	}
	body := map[string]any{
		"task_id": taskID,
		"md5":     fileMD5,
		"sha1":    fileSHA1,
	}
	var out struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			FID      string `json:"fid"`
			FileName string `json:"file_name"`
			Size     int64  `json:"size"`
			Finish   bool   `json:"finish"`
			ObjKey   string `json:"obj_key"`
		} `json:"data"`
	}
	if err := api.uploadPostJSON(ctx, api.uploadEndpoint("update/hash"), cookie, body, &out); err != nil {
		return UploadResult{}, false, err
	}
	if out.Status != 0 && out.Status != 200 {
		if api.uploadComponent() == "netdisk.uc" {
			return UploadResult{}, false, ucOperationError("upload_hash", out.Message, out.Status, out.Code)
		}
		return UploadResult{}, false, fmt.Errorf("%s upload hash failed: %s", api.uploadComponent(), firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	if out.Data.Finish {
		return UploadResult{
			FileID: firstNonEmpty(out.Data.FID, out.Data.ObjKey),
			Name:   firstNonEmpty(out.Data.FileName, filename),
			Size:   firstPositiveInt64Local(out.Data.Size, size),
		}, true, nil
	}
	return UploadResult{}, false, nil
}

func quarkLikeUploadParts(ctx context.Context, api quarkLikeUploadClient, cookie, localPath, filename string, size, chunkSize int64, pre quarkUploadPreResponse, progress uploadProgressFunc) ([]quarkOSSPart, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	partCount := int((size + chunkSize - 1) / chunkSize)
	if partCount <= 0 {
		partCount = 1
	}
	parts := make([]quarkOSSPart, 0, partCount)
	var uploaded int64
	applog.Info(api.uploadComponent(), "oss_upload_parts_start", map[string]any{
		"filename":   filename,
		"size":       size,
		"chunk_size": chunkSize,
		"part_count": partCount,
	})
	for index := 0; index < partCount; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := int64(index) * chunkSize
		length := chunkSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		data := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(io.NewSectionReader(file, offset, length), data); err != nil {
				return nil, err
			}
		}
		etag, err := quarkLikeUploadPart(ctx, api, cookie, pre, index+1, data)
		if err != nil {
			return nil, err
		}
		parts = append(parts, quarkOSSPart{Number: index + 1, ETag: etag})
		uploaded += int64(len(data))
		if progress != nil {
			progress(uploaded)
		}
	}
	applog.Info(api.uploadComponent(), "oss_upload_parts_finish", map[string]any{
		"filename":   filename,
		"uploaded":   uploaded,
		"part_count": len(parts),
	})
	return parts, nil
}

func quarkLikeUploadPart(ctx context.Context, api quarkLikeUploadClient, cookie string, pre quarkUploadPreResponse, partNumber int, data []byte) (string, error) {
	date := time.Now().UTC().Format(http.TimeFormat)
	headers := map[string]string{
		"Content-Type":     "application/octet-stream",
		"x-oss-date":       date,
		"x-oss-user-agent": quarkOSSUserAgent,
	}
	authMeta := quarkOSSUploadPartAuthMeta(pre, partNumber, "application/octet-stream", date)
	authKey, err := quarkLikeUploadAuth(ctx, api, cookie, pre.Data.TaskID, pre.Data.AuthInfo, authMeta)
	if err != nil {
		return "", err
	}
	endpoint, err := quarkOSSPartURL(pre, partNumber)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Referer", api.uploadReferer())
	resp, err := api.uploadHTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if api.uploadComponent() == "netdisk.uc" {
			return "", ucOperationError("oss_upload_part", strings.TrimSpace(string(detail)), resp.StatusCode, resp.StatusCode)
		}
		return "", fmt.Errorf("%s oss upload part failed with status %d: %s", api.uploadComponent(), resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		etag = hex.EncodeToString(md5Sum(data))
	}
	return etag, nil
}

func quarkLikeUploadAuth(ctx context.Context, api quarkLikeUploadClient, cookie, taskID, authInfo, authMeta string) (string, error) {
	body := map[string]any{
		"task_id":   taskID,
		"auth_info": authInfo,
		"auth_meta": authMeta,
	}
	var out struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AuthKey string `json:"auth_key"`
		} `json:"data"`
	}
	if err := api.uploadPostJSON(ctx, api.uploadEndpoint("upload/auth"), cookie, body, &out); err != nil {
		return "", err
	}
	if out.Status != 0 && out.Status != 200 {
		if api.uploadComponent() == "netdisk.uc" {
			return "", ucOperationError("upload_auth", out.Message, out.Status, out.Code)
		}
		return "", fmt.Errorf("%s upload auth failed: %s", api.uploadComponent(), firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	authKey := strings.TrimSpace(out.Data.AuthKey)
	if authKey == "" {
		if api.uploadComponent() == "netdisk.uc" {
			return "", errors.New("获取 UC 网盘上传授权失败：上游未返回授权密钥")
		}
		return "", fmt.Errorf("%s upload auth key is missing", api.uploadComponent())
	}
	return authKey, nil
}

func quarkLikeCompleteOSSUpload(ctx context.Context, api quarkLikeUploadClient, cookie string, pre quarkUploadPreResponse, parts []quarkOSSPart) error {
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	var body struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []struct {
			PartNumber int    `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
		} `xml:"Part"`
	}
	for _, part := range parts {
		body.Parts = append(body.Parts, struct {
			PartNumber int    `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
		}{PartNumber: part.Number, ETag: part.ETag})
	}
	xmlBody, err := xml.Marshal(body)
	if err != nil {
		return err
	}
	xmlBody = append([]byte(xml.Header), xmlBody...)
	contentMD5 := base64.StdEncoding.EncodeToString(md5Sum(xmlBody))
	date := time.Now().UTC().Format(http.TimeFormat)
	callback := pre.Data.Callback.HeaderValue()
	headers := map[string]string{
		"Content-MD5":      contentMD5,
		"Content-Type":     "application/xml",
		"x-oss-date":       date,
		"x-oss-user-agent": quarkOSSUserAgent,
	}
	if callback != "" {
		headers["x-oss-callback"] = callback
	}
	authMeta := quarkOSSCompleteAuthMeta(pre, contentMD5, callback, date)
	authKey, err := quarkLikeUploadAuth(ctx, api, cookie, pre.Data.TaskID, pre.Data.AuthInfo, authMeta)
	if err != nil {
		return err
	}
	endpoint, err := quarkOSSCompleteURL(pre)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(xmlBody))
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Referer", api.uploadReferer())
	resp, err := api.uploadHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if api.uploadComponent() == "netdisk.uc" {
			return ucOperationError("oss_complete", strings.TrimSpace(string(detail)), resp.StatusCode, resp.StatusCode)
		}
		return fmt.Errorf("%s oss complete failed with status %d: %s", api.uploadComponent(), resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}

func quarkLikeUploadFinish(ctx context.Context, api quarkLikeUploadClient, cookie, taskID, objKey, filename string, size int64) (UploadResult, error) {
	body := map[string]any{"task_id": taskID, "obj_key": objKey}
	var out struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			FID      string `json:"fid"`
			FileName string `json:"file_name"`
			Size     int64  `json:"size"`
			TaskID   string `json:"task_id"`
			Finish   bool   `json:"finish"`
		} `json:"data"`
	}
	if err := api.uploadPostJSON(ctx, api.uploadEndpoint("upload/finish"), cookie, body, &out); err != nil {
		return UploadResult{}, err
	}
	if out.Status != 0 && out.Status != 200 {
		if api.uploadComponent() == "netdisk.uc" {
			return UploadResult{}, ucOperationError("upload_finish", out.Message, out.Status, out.Code)
		}
		return UploadResult{}, fmt.Errorf("%s upload finish failed: %s", api.uploadComponent(), firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	return UploadResult{
		FileID: out.Data.FID,
		Name:   firstNonEmpty(out.Data.FileName, filename),
		Size:   firstPositiveInt64Local(out.Data.Size, size),
	}, nil
}

type quarkUploadPreResponse struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		TaskID    string              `json:"task_id"`
		UploadID  string              `json:"upload_id"`
		ObjKey    string              `json:"obj_key"`
		Bucket    string              `json:"bucket"`
		Region    string              `json:"region"`
		Callback  quarkUploadCallback `json:"callback"`
		AuthInfo  string              `json:"auth_info"`
		UploadURL string              `json:"upload_url"`
		FID       string              `json:"fid"`
		Finish    bool                `json:"finish"`
		PartSize  int64               `json:"part_size"`
	} `json:"data"`
	Metadata struct {
		PartSize          int64                `json:"part_size"`
		ChunkSize         int64                `json:"chunk_size"`
		MaxLimitRightInfo quarkUploadRightInfo `json:"max_limit_right_info"`
		SuggestRightInfo  quarkUploadRightInfo `json:"suggest_right_info"`
		SuggestMemberType string               `json:"suggest_member_type"`
	} `json:"metadata"`
}

type quarkUploadRightInfo struct {
	MemberType string `json:"member_type"`
	RightType  string `json:"right_type"`
	OpType     string `json:"op_type"`
	RightDesc  string `json:"right_desc"`
	RightKey   string `json:"right_key"`
	RightVal   string `json:"right_val"`
	App        string `json:"app"`
}

type quarkUploadCallback struct {
	raw json.RawMessage
}

func (c *quarkUploadCallback) UnmarshalJSON(data []byte) error {
	c.raw = append(c.raw[:0], data...)
	return nil
}

func (c quarkUploadCallback) HeaderValue() string {
	raw := bytes.TrimSpace(c.raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return ""
		}
		return strings.TrimSpace(value)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		compact.Write(raw)
	}
	return base64.StdEncoding.EncodeToString(compact.Bytes())
}

func (r quarkUploadPreResponse) PartSize() int64 {
	return firstPositiveInt64Local(r.Data.PartSize, r.Metadata.PartSize, r.Metadata.ChunkSize)
}

func (r quarkUploadPreResponse) LimitError(component string) error {
	if r.Code != 32004 {
		return nil
	}
	limit := parseUploadRightVal(r.Metadata.MaxLimitRightInfo.RightVal)
	suggestLimit := parseUploadRightVal(r.Metadata.SuggestRightInfo.RightVal)
	memberType := firstNonEmpty(r.Metadata.MaxLimitRightInfo.MemberType, "NORMAL")
	suggestMemberType := firstNonEmpty(r.Metadata.SuggestMemberType, r.Metadata.SuggestRightInfo.MemberType)
	if limit > 0 {
		message := fmt.Sprintf("%s 单文件上传大小超过当前账号限制，当前账号（%s）最多可上传 %s。", uploadComponentDisplayName(component), memberType, humanBytes(limit))
		if suggestLimit > limit && suggestMemberType != "" {
			message += fmt.Sprintf("开通 %s 后最高可上传 %s。", suggestMemberType, humanBytes(suggestLimit))
		}
		return errors.New(message)
	}
	return fmt.Errorf("%s upload pre failed: %s", component, firstNonEmpty(r.Message, strconv.Itoa(r.Status), strconv.Itoa(r.Code)))
}

func parseUploadRightVal(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func uploadComponentDisplayName(component string) string {
	switch strings.TrimSpace(component) {
	case "netdisk.uc":
		return "UC网盘"
	case "netdisk.quark":
		return "夸克网盘"
	default:
		return "网盘"
	}
}

func humanBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := float64(value)
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", value, units[unit])
	}
	if size == float64(int64(size)) {
		return fmt.Sprintf("%.0f %s", size, units[unit])
	}
	return fmt.Sprintf("%.2f %s", size, units[unit])
}

func (c *QuarkClient) quarkUploadPre(ctx context.Context, cookie, parentID, filename, localPath string, info os.FileInfo) (quarkUploadPreResponse, error) {
	now := time.Now().UnixMilli()
	mod := info.ModTime().UnixMilli()
	if mod <= 0 {
		mod = now
	}
	body := map[string]any{
		"ccp_hash_update": true,
		"dir_name":        "",
		"file_name":       filename,
		"format_type":     strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), "."),
		"l_created_at":    mod,
		"l_updated_at":    mod,
		"pdir_fid":        parentID,
		"size":            info.Size(),
		"source":          "desktop",
		"parallel_upload": true,
	}
	var out quarkUploadPreResponse
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/file/upload/pre?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return out, err
	}
	if out.Status != 0 && out.Status != 200 {
		if err := out.LimitError("netdisk.quark"); err != nil {
			return out, err
		}
		return out, fmt.Errorf("quark upload pre failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	if strings.TrimSpace(out.Data.TaskID) == "" && !out.Data.Finish {
		return out, errors.New("quark upload task id is missing")
	}
	applog.Info("netdisk.quark", "upload_pre_ready", map[string]any{
		"task_id":       out.Data.TaskID != "",
		"upload_id":     out.Data.UploadID != "",
		"bucket":        out.Data.Bucket != "",
		"region":        out.Data.Region != "",
		"obj_key":       out.Data.ObjKey != "",
		"callback":      out.Data.Callback.HeaderValue() != "",
		"finish":        out.Data.Finish,
		"part_size":     out.PartSize(),
		"has_auth_info": strings.TrimSpace(out.Data.AuthInfo) != "",
	})
	return out, nil
}

func (c *QuarkClient) quarkUploadHash(ctx context.Context, cookie, taskID, filename string, size int64, fileMD5, fileSHA1 string) (UploadResult, bool, error) {
	if strings.TrimSpace(taskID) == "" {
		return UploadResult{}, false, errors.New("quark upload task id is required")
	}
	body := map[string]any{
		"task_id": taskID,
		"md5":     fileMD5,
		"sha1":    fileSHA1,
	}
	var out struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			FID      string `json:"fid"`
			FileName string `json:"file_name"`
			Size     int64  `json:"size"`
			Finish   bool   `json:"finish"`
			ObjKey   string `json:"obj_key"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/file/update/hash?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return UploadResult{}, false, err
	}
	if out.Status != 0 && out.Status != 200 {
		return UploadResult{}, false, fmt.Errorf("quark upload hash failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	if out.Data.Finish {
		return UploadResult{
			FileID: firstNonEmpty(out.Data.FID, out.Data.ObjKey),
			Name:   firstNonEmpty(out.Data.FileName, filename),
			Size:   firstPositiveInt64Local(out.Data.Size, size),
		}, true, nil
	}
	return UploadResult{}, false, nil
}

type quarkOSSPart struct {
	Number int
	ETag   string
}

func (c *QuarkClient) quarkUploadParts(ctx context.Context, cookie, localPath, filename string, size, chunkSize int64, pre quarkUploadPreResponse, progress uploadProgressFunc) ([]quarkOSSPart, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	partCount := int((size + chunkSize - 1) / chunkSize)
	if partCount <= 0 {
		partCount = 1
	}
	parts := make([]quarkOSSPart, 0, partCount)
	var uploaded int64
	applog.Info("netdisk.quark", "oss_upload_parts_start", map[string]any{
		"filename":   filename,
		"size":       size,
		"chunk_size": chunkSize,
		"part_count": partCount,
	})
	for index := 0; index < partCount; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := int64(index) * chunkSize
		length := chunkSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		data := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(io.NewSectionReader(file, offset, length), data); err != nil {
				return nil, err
			}
		}
		etag, err := c.quarkUploadPart(ctx, cookie, pre, index+1, data)
		if err != nil {
			return nil, err
		}
		parts = append(parts, quarkOSSPart{Number: index + 1, ETag: etag})
		uploaded += int64(len(data))
		if progress != nil {
			progress(uploaded)
		}
	}
	applog.Info("netdisk.quark", "oss_upload_parts_finish", map[string]any{
		"filename":   filename,
		"uploaded":   uploaded,
		"part_count": len(parts),
	})
	return parts, nil
}

func (c *QuarkClient) quarkUploadPart(ctx context.Context, cookie string, pre quarkUploadPreResponse, partNumber int, data []byte) (string, error) {
	date := time.Now().UTC().Format(http.TimeFormat)
	headers := map[string]string{
		"Content-Type":     "application/octet-stream",
		"x-oss-date":       date,
		"x-oss-user-agent": quarkOSSUserAgent,
	}
	authMeta := quarkOSSUploadPartAuthMeta(pre, partNumber, "application/octet-stream", date)
	authKey, err := c.quarkUploadAuth(ctx, cookie, pre.Data.TaskID, pre.Data.AuthInfo, authMeta)
	if err != nil {
		return "", err
	}
	endpoint, err := quarkOSSPartURL(pre, partNumber)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Referer", "https://pan.quark.cn/")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("quark oss upload part failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		etag = hex.EncodeToString(md5Sum(data))
	}
	return etag, nil
}

func (c *QuarkClient) quarkUploadAuth(ctx context.Context, cookie, taskID, authInfo, authMeta string) (string, error) {
	body := map[string]any{
		"task_id":   taskID,
		"auth_info": authInfo,
		"auth_meta": authMeta,
	}
	var out struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AuthKey string `json:"auth_key"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/file/upload/auth?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return "", err
	}
	if out.Status != 0 && out.Status != 200 {
		return "", fmt.Errorf("quark upload auth failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	authKey := strings.TrimSpace(out.Data.AuthKey)
	if authKey == "" {
		return "", errors.New("quark upload auth key is missing")
	}
	return authKey, nil
}

func (c *QuarkClient) quarkCompleteOSSUpload(ctx context.Context, cookie string, pre quarkUploadPreResponse, parts []quarkOSSPart) error {
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	var body struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []struct {
			PartNumber int    `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
		} `xml:"Part"`
	}
	for _, part := range parts {
		body.Parts = append(body.Parts, struct {
			PartNumber int    `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
		}{PartNumber: part.Number, ETag: part.ETag})
	}
	xmlBody, err := xml.Marshal(body)
	if err != nil {
		return err
	}
	xmlBody = append([]byte(xml.Header), xmlBody...)
	contentMD5 := base64.StdEncoding.EncodeToString(md5Sum(xmlBody))
	date := time.Now().UTC().Format(http.TimeFormat)
	callback := pre.Data.Callback.HeaderValue()
	headers := map[string]string{
		"Content-MD5":      contentMD5,
		"Content-Type":     "application/xml",
		"x-oss-date":       date,
		"x-oss-user-agent": quarkOSSUserAgent,
	}
	if callback != "" {
		headers["x-oss-callback"] = callback
	}
	authMeta := quarkOSSCompleteAuthMeta(pre, contentMD5, callback, date)
	authKey, err := c.quarkUploadAuth(ctx, cookie, pre.Data.TaskID, pre.Data.AuthInfo, authMeta)
	if err != nil {
		return err
	}
	endpoint, err := quarkOSSCompleteURL(pre)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(xmlBody))
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Referer", "https://pan.quark.cn/")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("quark oss complete failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}

func (c *QuarkClient) quarkUploadFinish(ctx context.Context, cookie, taskID, objKey, filename string, size int64) (UploadResult, error) {
	body := map[string]any{"task_id": taskID, "obj_key": objKey}
	var out struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			FID      string `json:"fid"`
			FileName string `json:"file_name"`
			Size     int64  `json:"size"`
			TaskID   string `json:"task_id"`
			Finish   bool   `json:"finish"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/file/upload/finish?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return UploadResult{}, err
	}
	if out.Status != 0 && out.Status != 200 {
		return UploadResult{}, fmt.Errorf("quark upload finish failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	return UploadResult{
		FileID: out.Data.FID,
		Name:   firstNonEmpty(out.Data.FileName, filename),
		Size:   firstPositiveInt64Local(out.Data.Size, size),
	}, nil
}

func quarkOSSUploadPartAuthMeta(pre quarkUploadPreResponse, partNumber int, contentType, date string) string {
	return strings.Join([]string{
		http.MethodPut,
		"",
		contentType,
		date,
		"x-oss-date:" + date,
		"x-oss-user-agent:" + quarkOSSUserAgent,
		quarkOSSCanonicalResource(pre, "partNumber="+strconv.Itoa(partNumber)+"&uploadId="+pre.Data.UploadID),
	}, "\n")
}

func quarkOSSCompleteAuthMeta(pre quarkUploadPreResponse, contentMD5, callback, date string) string {
	lines := []string{
		http.MethodPost,
		contentMD5,
		"application/xml",
		date,
	}
	if callback != "" {
		lines = append(lines, "x-oss-callback:"+callback)
	}
	lines = append(lines,
		"x-oss-date:"+date,
		"x-oss-user-agent:"+quarkOSSUserAgent,
		quarkOSSCanonicalResource(pre, "uploadId="+pre.Data.UploadID),
	)
	return strings.Join(lines, "\n")
}

func quarkOSSCanonicalResource(pre quarkUploadPreResponse, query string) string {
	bucket := strings.TrimSpace(pre.Data.Bucket)
	objKey := strings.TrimLeft(pre.Data.ObjKey, "/")
	resource := "/" + bucket + "/" + objKey
	if strings.TrimSpace(query) != "" {
		resource += "?" + query
	}
	return resource
}

func quarkOSSPartURL(pre quarkUploadPreResponse, partNumber int) (string, error) {
	base, err := quarkOSSBaseURL(pre)
	if err != nil {
		return "", err
	}
	values := url.Values{}
	values.Set("partNumber", strconv.Itoa(partNumber))
	values.Set("uploadId", pre.Data.UploadID)
	return base + "?" + values.Encode(), nil
}

func quarkOSSCompleteURL(pre quarkUploadPreResponse) (string, error) {
	base, err := quarkOSSBaseURL(pre)
	if err != nil {
		return "", err
	}
	values := url.Values{}
	values.Set("uploadId", pre.Data.UploadID)
	return base + "?" + values.Encode(), nil
}

func quarkOSSBaseURL(pre quarkUploadPreResponse) (string, error) {
	objKey := strings.TrimLeft(pre.Data.ObjKey, "/")
	if objKey == "" {
		return "", errors.New("quark oss object key is missing")
	}
	bucket := strings.TrimSpace(pre.Data.Bucket)
	if uploadURL := strings.TrimSpace(pre.Data.UploadURL); uploadURL != "" {
		parsed, err := url.Parse(uploadURL)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			parsed.Scheme = "https"
			if bucket != "" && !strings.HasPrefix(parsed.Host, bucket+".") {
				parsed.Host = bucket + "." + parsed.Host
			}
			parsed.Path = joinEscapedURLPath(parsed.Path, objKey)
			parsed.RawQuery = ""
			return parsed.String(), nil
		}
	}
	region := strings.TrimSpace(pre.Data.Region)
	if bucket == "" || region == "" {
		return "", errors.New("quark oss bucket or region is missing")
	}
	return "https://" + bucket + "." + region + ".aliyuncs.com/" + escapeURLPath(objKey), nil
}

func joinEscapedURLPath(basePath, objKey string) string {
	basePath = strings.TrimRight(basePath, "/")
	escaped := escapeURLPath(strings.TrimLeft(objKey, "/"))
	if basePath == "" {
		return "/" + escaped
	}
	if strings.HasSuffix(basePath, escaped) {
		return basePath
	}
	return basePath + "/" + escaped
}

func escapeURLPath(value string) string {
	parts := strings.Split(value, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func quarkFileHashes(filePath string) (string, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	md5Hash := md5.New()
	sha1Hash := sha1.New()
	if _, err := io.Copy(io.MultiWriter(md5Hash, sha1Hash), file); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(md5Hash.Sum(nil)), hex.EncodeToString(sha1Hash.Sum(nil)), nil
}

func normalizeUploadRequest(req UploadFileRequest) (string, os.FileInfo, string, error) {
	localPath := strings.TrimSpace(req.LocalPath)
	if localPath == "" {
		return "", nil, "", errors.New("local path is required")
	}
	abs, err := filepath.Abs(filepath.Clean(localPath))
	if err == nil {
		localPath = abs
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return "", nil, "", err
	}
	if info.IsDir() {
		return "", nil, "", errors.New("local path must be a file")
	}
	filename := strings.TrimSpace(req.Name)
	if filename == "" {
		filename = info.Name()
	}
	filename = path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "." || filename == "/" || filename == "" {
		return "", nil, "", errors.New("file name is required")
	}
	return localPath, info, filename, nil
}

func md5Sum(data []byte) []byte {
	sum := md5.Sum(data)
	return sum[:]
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstPositiveInt64Local(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
