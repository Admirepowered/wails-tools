package netdisk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wailstools/internal/applog"
)

var (
	ErrAccountNotFound      = errors.New("netdisk account not found")
	ErrProviderUnsupported  = errors.New("netdisk provider unsupported")
	ErrCookieRequired       = errors.New("cookie is required")
	ErrCookieMissingFields  = errors.New("netdisk cookie missing required fields")
	ErrProviderUnauthorized = errors.New("netdisk provider unauthorized")
)

type ProviderClient interface {
	Probe(ctx context.Context, cookie string) (Account, error)
	ListFiles(ctx context.Context, account Account, cookie string, req ListFilesRequest) (FileList, error)
	ResolveDownload(ctx context.Context, account Account, cookie string, req ResolveDownloadRequest) (DownloadLink, error)
	CreateFolder(ctx context.Context, account Account, cookie string, req CreateFolderRequest) error
	RenameFile(ctx context.Context, account Account, cookie string, req RenameFileRequest) error
	DeleteFiles(ctx context.Context, account Account, cookie string, req DeleteFilesRequest) error
	CopyFiles(ctx context.Context, account Account, cookie string, req FileTransferRequest) error
	MoveFiles(ctx context.Context, account Account, cookie string, req FileTransferRequest) error
	CreateShare(ctx context.Context, account Account, cookie string, req CreateShareRequest) (ShareResult, error)
	ListAccountShares(ctx context.Context, account Account, cookie string, req ListAccountSharesRequest) (AccountShareList, error)
	CancelShares(ctx context.Context, account Account, cookie string, req CancelSharesRequest) error
	ListRecycle(ctx context.Context, account Account, cookie string, req ListRecycleRequest) (RecycleList, error)
	RestoreRecycle(ctx context.Context, account Account, cookie string, req RecycleMutationRequest) error
	DeleteRecycle(ctx context.Context, account Account, cookie string, req RecycleMutationRequest) error
}

type ProviderUploader interface {
	UploadFile(ctx context.Context, account Account, cookie string, req UploadFileRequest, progress func(uploaded int64)) (UploadResult, error)
}

type providerSearcher interface {
	SearchFiles(ctx context.Context, account Account, cookie string, req SearchFilesRequest) (FileList, error)
}

type videoPreviewProvider interface {
	ResolveVideoPreview(ctx context.Context, account Account, cookie string, req VideoPreviewRequest) (VideoPreviewResult, error)
}

type accountPersistence interface {
	SaveAccounts([]Account) error
	LoadCookie(string) (string, error)
	SaveCookie(string, string) error
	DeleteCookie(string) error
}

type Manager struct {
	mu sync.RWMutex
	// Authentication mutations take authCommitMu before touching Store or mu.
	// Manager.mu is never held while acquiring the Store cookie lock.
	authCommitMu sync.Mutex
	store        accountPersistence
	accounts     map[string]Account
	// providerMu guards providers so SetHTTPClient can rebuild the provider
	// clients while other goroutines keep calling provider().
	providerMu sync.RWMutex
	providers  map[Provider]ProviderClient
}

func NewManager(store *Store) (*Manager, error) {
	return NewManagerWithHTTPClient(store, nil)
}

// NewManagerWithHTTPClient behaves like NewManager but routes every provider
// client built by the manager through client (for example an http.Client with a
// proxy transport). A nil client keeps the http.DefaultClient behaviour.
func NewManagerWithHTTPClient(store *Store, client *http.Client) (*Manager, error) {
	var persistence accountPersistence
	if store != nil {
		persistence = store
	}
	m := &Manager{
		store:     persistence,
		accounts:  make(map[string]Account),
		providers: newProviderClients(client),
	}
	if store != nil {
		accounts, err := store.LoadAccounts()
		if err != nil {
			return nil, err
		}
		changed := false
		for _, account := range accounts {
			if account.ID != "" {
				if account.IdentityHash != "" && invalidIdentityHash(account.Provider, account.IdentityHash) {
					account.IdentityHash = ""
					changed = true
				}
				if cookie, err := store.LoadCookie(account.ID); err == nil && cookie != "" {
					cookieHash := hashProviderCookie(account.Provider, cookie)
					if account.CookieHash != cookieHash {
						account.CookieHash = cookieHash
						changed = true
					}
					cookieHint := cookieHintForProvider(account.Provider, cookie)
					if account.CookieHint != cookieHint {
						account.CookieHint = cookieHint
						changed = true
					}
				}
				m.accounts[account.ID] = account
			}
		}
		if changed {
			if err := m.persistLocked(); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// newProviderClients builds the provider map. A nil client means
// http.DefaultClient, which matches the original NewManager behaviour.
func newProviderClients(client *http.Client) map[Provider]ProviderClient {
	if client == nil {
		client = http.DefaultClient
	}
	return map[Provider]ProviderClient{
		ProviderBaidu: NewBaiduClient(client),
		ProviderQuark: NewQuarkClient(client),
		ProviderUC:    NewUCClient(client),
	}
}

// SetHTTPClient replaces the HTTP client used by every provider client created
// by the manager. Passing nil restores the default http.DefaultClient
// behaviour. Provider request logic is unchanged.
func (m *Manager) SetHTTPClient(client *http.Client) {
	if m == nil {
		return
	}
	providers := newProviderClients(client)
	m.providerMu.Lock()
	m.providers = providers
	m.providerMu.Unlock()
}

func (m *Manager) ListAccounts() []Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	accounts := make([]Account, 0, len(m.accounts))
	for _, account := range m.accounts {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Provider == accounts[j].Provider {
			return accounts[i].CreatedAt.Before(accounts[j].CreatedAt)
		}
		return accounts[i].Provider < accounts[j].Provider
	})
	return accounts
}

func (m *Manager) FirstCookie(provider Provider) (string, bool) {
	_, cookie, ok := m.FirstAccountWithCookie(provider)
	return cookie, ok
}

func (m *Manager) FirstAccountWithCookie(provider Provider) (Account, string, bool) {
	provider = normalizeProvider(provider)
	m.mu.RLock()
	accounts := make([]Account, 0, len(m.accounts))
	for _, account := range m.accounts {
		if account.Provider == provider && account.Status == AccountStatusActive {
			accounts = append(accounts, account)
		}
	}
	m.mu.RUnlock()
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].CreatedAt.Before(accounts[j].CreatedAt)
	})
	for _, account := range accounts {
		if m.store == nil {
			return Account{}, "", false
		}
		cookie, err := m.store.LoadCookie(account.ID)
		if err == nil && strings.TrimSpace(cookie) != "" {
			return account, cookie, true
		}
	}
	return Account{}, "", false
}

func (m *Manager) AccountWithCookie(accountID string) (Account, string, error) {
	return m.accountWithCookie(accountID)
}

func (m *Manager) AddAccount(ctx context.Context, req AddAccountRequest) (Account, error) {
	req.Provider = normalizeProvider(req.Provider)
	req.Cookie = strings.TrimSpace(req.Cookie)
	if req.Cookie == "" {
		return Account{}, ErrCookieRequired
	}
	if err := validateProviderCookie(req.Provider, req.Cookie); err != nil {
		return Account{}, err
	}
	provider, err := m.provider(req.Provider)
	if err != nil {
		return Account{}, err
	}
	probeCookie := req.Cookie
	if supportsResponseCookieRefresh(req.Provider) {
		ctx = withQuarkResponseCookieCapture(ctx, func(cookies []*http.Cookie) {
			if value := quarkResponseCookieValue(cookies, "__puus"); value != "" {
				probeCookie, _ = mergeCookieField(probeCookie, "__puus", value)
			}
		})
	}
	probed, err := provider.Probe(ctx, req.Cookie)
	if err != nil {
		return Account{}, err
	}
	if supportsResponseCookieRefresh(req.Provider) {
		probeCookie = strings.TrimSpace(probeCookie)
	}
	now := time.Now().UTC()
	cookieToSave := firstNonEmpty(probeCookie, req.Cookie)
	cookieHash := hashProviderCookie(req.Provider, cookieToSave)

	// Provider probes stay outside the lock. Identity resolution and persistence
	// run inside one serialized boundary; failed metadata writes are compensated
	// back to the account and cookie snapshots captured below.
	m.authCommitMu.Lock()
	defer m.authCommitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	existingID, err := m.findExistingAccountID(ctx, req.Provider, cookieHash, probed.IdentityHash)
	if err != nil {
		return Account{}, err
	}
	var (
		previousAccount    Account
		previousCookie     string
		hadPreviousAccount bool
	)
	if existingID != "" {
		previousAccount, hadPreviousAccount = m.accountByID(existingID)
		if !hadPreviousAccount {
			return Account{}, ErrAccountNotFound
		}
		if m.store != nil {
			previousCookie, err = m.store.LoadCookie(existingID)
			if err != nil {
				return Account{}, err
			}
		}
	}
	probed.ID = uuid.NewString()
	if existingID != "" {
		probed.ID = existingID
	}
	probed.Provider = req.Provider
	if strings.TrimSpace(req.DisplayName) != "" {
		probed.Remark = strings.TrimSpace(req.DisplayName)
	}
	if hadPreviousAccount {
		probed.Remark = firstNonEmpty(probed.Remark, previousAccount.Remark)
		probed.UK = firstNonEmpty(probed.UK, previousAccount.UK)
	}
	if probed.DisplayName == "" {
		probed.DisplayName = defaultProviderName(req.Provider)
	}
	probed.Status = AccountStatusActive
	probed.CreatedAt = now
	if hadPreviousAccount && !previousAccount.CreatedAt.IsZero() {
		probed.CreatedAt = previousAccount.CreatedAt
	}
	probed.UpdatedAt = now
	probed.LastSyncAt = &now
	probed.CookieHint = cookieHintForProvider(req.Provider, cookieToSave)
	probed.CookieHash = cookieHash
	probed.IdentityHash = strings.TrimSpace(probed.IdentityHash)
	if len(probed.Capabilities) == 0 {
		probed.Capabilities = capabilities(req.Provider)
	}

	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	if m.store != nil {
		if err := m.store.SaveCookie(probed.ID, cookieToSave); err != nil {
			return Account{}, err
		}
	}
	m.mu.Lock()
	m.accounts[probed.ID] = probed
	err = m.persistLocked()
	if err != nil {
		if hadPreviousAccount {
			m.accounts[probed.ID] = previousAccount
		} else {
			delete(m.accounts, probed.ID)
		}
	}
	m.mu.Unlock()
	if err != nil {
		rollbackErr := m.rollbackAuthenticationCookie(probed.ID, previousCookie, hadPreviousAccount)
		return Account{}, errors.Join(err, rollbackErr)
	}
	return probed, nil
}

func (m *Manager) UpdateAccountCookie(ctx context.Context, accountID string, req AddAccountRequest) (Account, error) {
	account, _, err := m.accountWithCookie(accountID)
	if err != nil {
		return Account{}, err
	}
	req.Provider = normalizeProvider(req.Provider)
	req.Cookie = strings.TrimSpace(req.Cookie)
	if req.Provider != account.Provider {
		return Account{}, fmt.Errorf("account provider mismatch")
	}
	if req.Cookie == "" {
		return Account{}, ErrCookieRequired
	}
	if err := validateProviderCookie(req.Provider, req.Cookie); err != nil {
		return Account{}, err
	}
	provider, err := m.provider(req.Provider)
	if err != nil {
		return Account{}, err
	}
	probeCookie := req.Cookie
	if supportsResponseCookieRefresh(req.Provider) {
		ctx = withQuarkResponseCookieCapture(ctx, func(cookies []*http.Cookie) {
			if value := quarkResponseCookieValue(cookies, "__puus"); value != "" {
				probeCookie, _ = mergeCookieField(probeCookie, "__puus", value)
			}
		})
	}
	probed, err := provider.Probe(ctx, req.Cookie)
	if err != nil {
		return Account{}, err
	}

	// Reload the account after entering the serialized commit boundary so a
	// concurrent add/relogin cannot be overwritten by the pre-probe snapshot.
	m.authCommitMu.Lock()
	defer m.authCommitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	account, previousCookie, err := m.accountWithCookie(accountID)
	if err != nil {
		return Account{}, err
	}
	if req.Provider != account.Provider {
		return Account{}, fmt.Errorf("account provider mismatch")
	}
	previousAccount := account
	now := time.Now().UTC()
	cookieToSave := firstNonEmpty(strings.TrimSpace(probeCookie), req.Cookie)
	account.DisplayName = firstNonEmpty(probed.DisplayName, account.DisplayName)
	account.UK = firstNonEmpty(probed.UK, account.UK)
	account.AvatarURL = firstNonEmpty(probed.AvatarURL, account.AvatarURL)
	account.MemberType = firstNonEmpty(probed.MemberType, account.MemberType)
	account.IdentityHash = firstNonEmpty(probed.IdentityHash, account.IdentityHash)
	account.UsedBytes = probed.UsedBytes
	account.TotalBytes = probed.TotalBytes
	account.Status = AccountStatusActive
	account.Error = ""
	account.UpdatedAt = now
	account.LastSyncAt = &now
	account.CookieHint = cookieHintForProvider(req.Provider, cookieToSave)
	account.CookieHash = hashProviderCookie(req.Provider, cookieToSave)
	if strings.TrimSpace(req.DisplayName) != "" {
		account.Remark = strings.TrimSpace(req.DisplayName)
	}
	if len(probed.Capabilities) > 0 {
		account.Capabilities = probed.Capabilities
	}

	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	if m.store != nil {
		if err := m.store.SaveCookie(account.ID, cookieToSave); err != nil {
			return Account{}, err
		}
	}
	m.mu.Lock()
	m.accounts[account.ID] = account
	err = m.persistLocked()
	if err != nil {
		m.accounts[account.ID] = previousAccount
	}
	m.mu.Unlock()
	if err != nil {
		rollbackErr := m.rollbackAuthenticationCookie(account.ID, previousCookie, true)
		return Account{}, errors.Join(err, rollbackErr)
	}
	return account, nil
}

func (m *Manager) RefreshAccount(ctx context.Context, accountID string) (Account, error) {
	account, cookie, err := m.accountWithCookie(accountID)
	if err != nil {
		return Account{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return Account{}, err
	}
	probeCtx := m.withProviderCookieRefresh(ctx, account, cookie)
	probed, err := provider.Probe(probeCtx, cookie)
	probeCredentialVersion := hashCredentialVersion(quarkCookieWithCapturedResponseFields(probeCtx, cookie))

	// Keep the provider network request outside the commit lock. Removal or a
	// relogin may finish while the probe is in flight, so the account snapshot
	// must be reloaded under the same boundary used by those mutations before
	// applying the result.
	m.authCommitMu.Lock()
	defer m.authCommitMu.Unlock()
	committedCookie, cookieErr := m.store.LoadCookie(accountID)
	if cookieErr != nil {
		return Account{}, cookieErr
	}
	m.mu.Lock()
	current, ok := m.accounts[accountID]
	if !ok {
		m.mu.Unlock()
		return Account{}, ErrAccountNotFound
	}
	if current.Provider != account.Provider || hashCredentialVersion(committedCookie) != probeCredentialVersion {
		m.mu.Unlock()
		return current, nil
	}
	account = current
	now := time.Now().UTC()
	account.UpdatedAt = now
	account.LastSyncAt = &now
	if err != nil {
		if errors.Is(err, ErrProviderUnauthorized) {
			account.Status = AccountStatusError
			account.Error = err.Error()
		}
	} else {
		account.DisplayName = firstNonEmpty(probed.DisplayName, account.DisplayName)
		account.UK = firstNonEmpty(probed.UK, account.UK)
		account.AvatarURL = firstNonEmpty(probed.AvatarURL, account.AvatarURL)
		account.MemberType = firstNonEmpty(probed.MemberType, account.MemberType)
		account.IdentityHash = firstNonEmpty(probed.IdentityHash, account.IdentityHash)
		account.UsedBytes = probed.UsedBytes
		account.TotalBytes = probed.TotalBytes
		account.Status = AccountStatusActive
		account.Error = ""
		if len(probed.Capabilities) > 0 {
			account.Capabilities = probed.Capabilities
		}
	}
	m.accounts[account.ID] = account
	persistErr := m.persistLocked()
	m.mu.Unlock()
	if err != nil {
		return account, err
	}
	return account, persistErr
}

func (m *Manager) UpdateAccountRemark(ctx context.Context, accountID, remark string) (Account, error) {
	remark = strings.TrimSpace(remark)
	m.mu.Lock()
	account, ok := m.accounts[accountID]
	if !ok {
		m.mu.Unlock()
		return Account{}, ErrAccountNotFound
	}
	account.Remark = remark
	account.UpdatedAt = time.Now().UTC()
	m.accounts[accountID] = account
	err := m.persistLocked()
	m.mu.Unlock()
	if err != nil {
		return account, err
	}
	if remark != "" {
		return account, nil
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	refreshed, refreshErr := m.RefreshAccount(refreshCtx, accountID)
	if refreshErr != nil {
		return account, nil
	}
	return refreshed, nil
}

func (m *Manager) RemoveAccount(accountID string) error {
	m.authCommitMu.Lock()
	defer m.authCommitMu.Unlock()

	m.mu.RLock()
	_, exists := m.accounts[accountID]
	m.mu.RUnlock()
	if !exists {
		return ErrAccountNotFound
	}

	var previousCookie string
	if m.store != nil {
		var err error
		previousCookie, err = m.store.LoadCookie(accountID)
		if err != nil {
			return fmt.Errorf("load account cookie before removal: %w", err)
		}
	}

	m.mu.Lock()
	previousAccount, ok := m.accounts[accountID]
	if !ok {
		m.mu.Unlock()
		return ErrAccountNotFound
	}
	delete(m.accounts, accountID)
	persistErr := m.persistLocked()
	if persistErr != nil {
		m.accounts[accountID] = previousAccount
		rollbackErr := m.persistLocked()
		m.mu.Unlock()
		if rollbackErr != nil {
			rollbackErr = fmt.Errorf("rollback account metadata after removal failure: %w", rollbackErr)
		}
		return errors.Join(
			fmt.Errorf("persist account removal: %w", persistErr),
			rollbackErr,
		)
	}
	m.mu.Unlock()
	if m.store == nil {
		return nil
	}

	deleteErr := m.store.DeleteCookie(accountID)
	if deleteErr == nil {
		return nil
	}

	m.mu.Lock()
	m.accounts[accountID] = previousAccount
	accountRollbackErr := m.persistLocked()
	m.mu.Unlock()
	if accountRollbackErr != nil {
		accountRollbackErr = fmt.Errorf("rollback account metadata after cookie deletion failure: %w", accountRollbackErr)
	}
	var cookieRollbackErr error
	if previousCookie != "" {
		if err := m.store.SaveCookie(accountID, previousCookie); err != nil {
			cookieRollbackErr = fmt.Errorf("rollback account cookie after deletion failure: %w", err)
		}
	}
	return errors.Join(
		fmt.Errorf("delete account cookie: %w", deleteErr),
		accountRollbackErr,
		cookieRollbackErr,
	)
}

func (m *Manager) ListFiles(ctx context.Context, req ListFilesRequest) (FileList, error) {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return FileList{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return FileList{}, err
	}
	files, err := provider.ListFiles(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	if err != nil {
		return FileList{}, err
	}
	return files, nil
}

func (m *Manager) SearchFiles(ctx context.Context, req SearchFilesRequest) (FileList, error) {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return FileList{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return FileList{}, err
	}
	searcher, ok := provider.(providerSearcher)
	if !ok {
		return FileList{}, ErrProviderUnsupported
	}
	files, err := searcher.SearchFiles(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	if err != nil {
		return FileList{}, err
	}
	return files, nil
}

func (m *Manager) ResolveDownload(ctx context.Context, req ResolveDownloadRequest) (DownloadLink, error) {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return DownloadLink{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return DownloadLink{}, err
	}
	link, err := provider.ResolveDownload(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return link, err
}

func (m *Manager) ResolveVideoPreview(ctx context.Context, req VideoPreviewRequest) (VideoPreviewResult, error) {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return VideoPreviewResult{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return VideoPreviewResult{}, err
	}
	resolver, ok := provider.(videoPreviewProvider)
	if !ok {
		return VideoPreviewResult{}, ErrProviderUnsupported
	}
	result, err := resolver.ResolveVideoPreview(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return result, err
}

func (m *Manager) CreateFolder(ctx context.Context, req CreateFolderRequest) error {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return err
	}
	err = provider.CreateFolder(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return err
}

func (m *Manager) RenameFile(ctx context.Context, req RenameFileRequest) error {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return err
	}
	err = provider.RenameFile(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return err
}

func (m *Manager) DeleteFiles(ctx context.Context, req DeleteFilesRequest) error {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return err
	}
	err = provider.DeleteFiles(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return err
}

func (m *Manager) CopyFiles(ctx context.Context, req FileTransferRequest) error {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return err
	}
	err = provider.CopyFiles(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return err
}

func (m *Manager) MoveFiles(ctx context.Context, req FileTransferRequest) error {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return err
	}
	err = provider.MoveFiles(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return err
}

func (m *Manager) UploadFile(ctx context.Context, req UploadFileRequest, progress func(uploaded int64)) (UploadResult, error) {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return UploadResult{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return UploadResult{}, err
	}
	uploader, ok := provider.(ProviderUploader)
	if !ok {
		return UploadResult{}, ErrProviderUnsupported
	}
	result, err := uploader.UploadFile(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req, progress)
	m.updateAccountAfterProviderResult(account, err)
	return result, err
}

func (m *Manager) CreateShare(ctx context.Context, req CreateShareRequest) (ShareResult, error) {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return ShareResult{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return ShareResult{}, err
	}
	result, err := provider.CreateShare(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return result, err
}

func (m *Manager) ListAccountShares(ctx context.Context, req ListAccountSharesRequest) (AccountShareList, error) {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return AccountShareList{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return AccountShareList{}, err
	}
	result, err := provider.ListAccountShares(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return result, err
}

func (m *Manager) CancelShares(ctx context.Context, req CancelSharesRequest) error {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return err
	}
	err = provider.CancelShares(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return err
}

func (m *Manager) ListRecycle(ctx context.Context, req ListRecycleRequest) (RecycleList, error) {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return RecycleList{}, err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return RecycleList{}, err
	}
	result, err := provider.ListRecycle(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return result, err
}

func (m *Manager) RestoreRecycle(ctx context.Context, req RecycleMutationRequest) error {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return err
	}
	err = provider.RestoreRecycle(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return err
}

func (m *Manager) DeleteRecycle(ctx context.Context, req RecycleMutationRequest) error {
	account, cookie, err := m.accountWithCookie(req.AccountID)
	if err != nil {
		return err
	}
	provider, err := m.provider(account.Provider)
	if err != nil {
		return err
	}
	err = provider.DeleteRecycle(m.withProviderCookieRefresh(ctx, account, cookie), account, cookie, req)
	m.updateAccountAfterProviderResult(account, err)
	return err
}

func (m *Manager) updateAccountAfterProviderResult(account Account, providerErr error) {
	if account.ID == "" {
		return
	}
	now := time.Now().UTC()
	changed := false

	m.mu.Lock()
	current, ok := m.accounts[account.ID]
	if !ok {
		m.mu.Unlock()
		return
	}
	if errors.Is(providerErr, ErrProviderUnauthorized) {
		message := "网盘登录凭证已失效，请重新登录"
		if current.Status != AccountStatusError || current.Error != message {
			current.Status = AccountStatusError
			current.Error = message
			current.UpdatedAt = now
			changed = true
		}
	} else if providerErr == nil && (current.Status != AccountStatusActive || current.Error != "") {
		current.Status = AccountStatusActive
		current.Error = ""
		current.UpdatedAt = now
		changed = true
	}
	if changed {
		m.accounts[account.ID] = current
		_ = m.persistLocked()
	}
	m.mu.Unlock()
}

func (m *Manager) withProviderCookieRefresh(ctx context.Context, account Account, cookie string) context.Context {
	if !supportsResponseCookieRefresh(account.Provider) || m.store == nil {
		return ctx
	}
	expectedCookieHash := hashProviderCookie(account.Provider, cookie)
	expectedCredentialVersion := hashCredentialVersion(cookie)
	var credentialVersionMu sync.Mutex
	return withQuarkResponseCookieCapture(ctx, func(cookies []*http.Cookie) {
		value := quarkResponseCookieValue(cookies, "__puus")
		if value == "" {
			return
		}
		credentialVersionMu.Lock()
		nextCredentialVersion, err := m.mergeStoredCookieField(account.ID, account.Provider, expectedCookieHash, expectedCredentialVersion, "__puus", value)
		if nextCredentialVersion != "" {
			expectedCredentialVersion = nextCredentialVersion
		}
		credentialVersionMu.Unlock()
		if err != nil {
			applog.Warn("netdisk", "refresh_response_cookie_failed", err.Error(), map[string]any{
				"account_id": account.ID,
				"provider":   account.Provider,
				"field":      "__puus",
			})
		}
	})
}

func supportsResponseCookieRefresh(provider Provider) bool {
	provider = normalizeProvider(provider)
	return provider == ProviderQuark || provider == ProviderUC
}

func (m *Manager) mergeStoredCookieField(accountID string, provider Provider, expectedCookieHash, expectedCredentialVersion, name, value string) (string, error) {
	if m.store == nil {
		return "", nil
	}
	m.authCommitMu.Lock()
	defer m.authCommitMu.Unlock()
	m.mu.RLock()
	account, exists := m.accounts[accountID]
	m.mu.RUnlock()
	if !exists {
		return "", nil
	}
	if account.Provider != provider || (account.CookieHash != "" && account.CookieHash != expectedCookieHash) {
		return "", nil
	}
	cookie, err := m.store.LoadCookie(accountID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cookie) == "" {
		return "", ErrCookieRequired
	}
	if hashCredentialVersion(cookie) != expectedCredentialVersion {
		return "", nil
	}
	merged, changed := mergeCookieField(cookie, name, value)
	if !changed {
		return "", nil
	}
	if err := m.store.SaveCookie(accountID, merged); err != nil {
		return "", err
	}
	nextCredentialVersion := hashCredentialVersion(merged)
	now := time.Now().UTC()
	m.mu.Lock()
	if account, ok := m.accounts[accountID]; ok {
		account.CookieHash = hashProviderCookie(provider, merged)
		account.CookieHint = cookieHintForProvider(provider, merged)
		account.UpdatedAt = now
		m.accounts[accountID] = account
		err = m.persistLocked()
	}
	m.mu.Unlock()
	if err != nil {
		return nextCredentialVersion, err
	}
	applog.Info("netdisk", "quark_cookie_refreshed", map[string]any{
		"account_id": accountID,
		"field":      name,
	})
	return nextCredentialVersion, nil
}

func (m *Manager) rollbackAuthenticationCookie(accountID, previousCookie string, hadPreviousAccount bool) error {
	if m.store == nil {
		return nil
	}
	var err error
	if hadPreviousAccount && previousCookie != "" {
		err = m.store.SaveCookie(accountID, previousCookie)
	} else {
		err = m.store.DeleteCookie(accountID)
	}
	if err != nil {
		return fmt.Errorf("rollback authentication cookie: %w", err)
	}
	return nil
}

func (m *Manager) provider(provider Provider) (ProviderClient, error) {
	if provider == "" {
		provider = ProviderBaidu
	}
	m.providerMu.RLock()
	client, ok := m.providers[provider]
	m.providerMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderUnsupported, provider)
	}
	return client, nil
}

func (m *Manager) accountByID(accountID string) (Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	account, ok := m.accounts[accountID]
	return account, ok
}

func (m *Manager) findAccountIDByCookieHash(provider Provider, cookieHash string) string {
	if cookieHash == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, account := range m.accounts {
		if account.Provider == provider && account.CookieHash == cookieHash {
			return account.ID
		}
	}
	return ""
}

// findExistingAccountID runs inside authCommitMu. The legacy probe is lookup
// only; AddAccount persists the resolved identity and cookie together.
func (m *Manager) findExistingAccountID(ctx context.Context, provider Provider, cookieHash string, identityHashValue string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if existingID := m.findAccountIDByIdentityHash(provider, identityHashValue); existingID != "" {
		return existingID, nil
	}
	if existingID := m.findAccountIDByCookieHash(provider, cookieHash); existingID != "" {
		return existingID, nil
	}
	if strings.TrimSpace(identityHashValue) == "" || m.store == nil {
		return "", nil
	}
	return m.findLegacyAccountIDByProbedIdentity(ctx, provider, identityHashValue)
}

func (m *Manager) findAccountIDByIdentityHash(provider Provider, identityHashValue string) string {
	identityHashValue = strings.TrimSpace(identityHashValue)
	if identityHashValue == "" || invalidIdentityHash(provider, identityHashValue) {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, account := range m.accounts {
		accountIdentityHash := strings.TrimSpace(account.IdentityHash)
		if invalidIdentityHash(account.Provider, accountIdentityHash) {
			continue
		}
		if account.Provider == provider && accountIdentityHash == identityHashValue {
			return account.ID
		}
	}
	return ""
}

func (m *Manager) findLegacyAccountIDByProbedIdentity(ctx context.Context, provider Provider, identityHashValue string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	providerClient, err := m.provider(provider)
	if err != nil {
		return "", err
	}
	m.mu.RLock()
	candidates := make([]Account, 0, len(m.accounts))
	for _, account := range m.accounts {
		if account.Provider == provider && strings.TrimSpace(account.IdentityHash) == "" {
			candidates = append(candidates, account)
		}
	}
	m.mu.RUnlock()
	for _, account := range candidates {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		cookie, err := m.store.LoadCookie(account.ID)
		if err != nil {
			return "", fmt.Errorf("load legacy account cookie: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if strings.TrimSpace(cookie) == "" {
			continue
		}
		probed, err := providerClient.Probe(ctx, cookie)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if err != nil || strings.TrimSpace(probed.IdentityHash) != identityHashValue {
			continue
		}
		return account.ID, nil
	}
	return "", nil
}

func (m *Manager) accountWithCookie(accountID string) (Account, string, error) {
	m.mu.RLock()
	account, ok := m.accounts[accountID]
	m.mu.RUnlock()
	if !ok {
		return Account{}, "", ErrAccountNotFound
	}
	if m.store == nil {
		return Account{}, "", errors.New("netdisk account store is not configured")
	}
	cookie, err := m.store.LoadCookie(accountID)
	if err != nil {
		return Account{}, "", err
	}
	if cookie == "" {
		return Account{}, "", ErrCookieRequired
	}
	if err := validateProviderCookie(account.Provider, cookie); err != nil {
		return Account{}, "", err
	}
	return account, cookie, nil
}

func (m *Manager) persistLocked() error {
	if m.store == nil {
		return nil
	}
	accounts := make([]Account, 0, len(m.accounts))
	for _, account := range m.accounts {
		accounts = append(accounts, account)
	}
	return m.store.SaveAccounts(accounts)
}

func defaultProviderName(provider Provider) string {
	switch provider {
	case ProviderBaidu:
		return "Baidu Netdisk"
	case ProviderQuark:
		return "Quark Cloud Drive"
	case ProviderUC:
		return "UC Cloud Drive"
	default:
		return string(provider)
	}
}

func normalizeProvider(provider Provider) Provider {
	if provider == "" {
		return ProviderBaidu
	}
	return provider
}

func validateProviderCookie(provider Provider, cookie string) error {
	required := requiredCookieFields(provider)
	if len(required) == 0 {
		return nil
	}
	fields := parseCookieFields(cookie)
	missing := make([]string, 0, len(required))
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrCookieMissingFields, strings.Join(missing, ", "))
	}
	return nil
}

func requiredCookieFields(provider Provider) []string {
	switch normalizeProvider(provider) {
	case ProviderBaidu:
		return []string{"BDUSS", "STOKEN"}
	case ProviderQuark:
		return []string{"__pus"}
	case ProviderUC:
		return []string{"__pus"}
	default:
		return nil
	}
}

func capabilities(provider Provider) []string {
	switch provider {
	case ProviderBaidu:
		return []string{"files", "quota", "share", "recycle", "download", "upload"}
	case ProviderQuark:
		return []string{"files", "quota", "share", "recycle", "download", "upload"}
	case ProviderUC:
		return []string{"files", "quota", "share", "recycle", "download", "upload"}
	default:
		return []string{"files"}
	}
}

func cookieHint(cookie string) string {
	cookie = strings.TrimSpace(cookie)
	if len(cookie) <= 18 {
		return cookie
	}
	return cookie[:10] + "..." + cookie[len(cookie)-6:]
}

func cookieHintForProvider(provider Provider, cookie string) string {
	if provider := normalizeProvider(provider); provider == ProviderQuark || provider == ProviderUC {
		return cookieHint(cookieWithoutFields(cookie, "__puus"))
	}
	return cookieHint(cookie)
}

func hashProviderCookie(provider Provider, cookie string) string {
	if provider := normalizeProvider(provider); provider == ProviderQuark || provider == ProviderUC {
		return hashCookieWithout(cookie, "__puus")
	}
	return hashCookie(cookie)
}

// hashCredentialVersion fingerprints the complete credential snapshot used by
// an in-flight provider request. Unlike hashProviderCookie, it must include
// rotating response fields such as Quark/UC __puus so an older request cannot
// commit over a newer relogin that changed only that field.
func hashCredentialVersion(cookie string) string {
	return hashCookie(cookie)
}

func identityHash(provider Provider, identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" || !isMeaningfulIdentity(identity) {
		return ""
	}
	sum := sha256.Sum256([]byte(string(normalizeProvider(provider)) + ":" + identity))
	return hex.EncodeToString(sum[:])
}

func invalidIdentityHash(provider Provider, hash string) bool {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return false
	}
	return hash == identityHashRaw(provider, "<nil>") ||
		hash == identityHashRaw(provider, "0") ||
		hash == identityHashRaw(provider, "null") ||
		hash == identityHashRaw(provider, "undefined") ||
		hash == identityHashRaw(provider, "none")
}

func identityHashRaw(provider Provider, identity string) string {
	sum := sha256.Sum256([]byte(string(normalizeProvider(provider)) + ":" + identity))
	return hex.EncodeToString(sum[:])
}

func isMeaningfulIdentity(identity string) bool {
	return firstNonEmptyMeaningful(identity) != ""
}

func hashCookie(cookie string) string {
	sum := sha256.Sum256([]byte(normalizeCookieForHash(cookie)))
	return hex.EncodeToString(sum[:])
}

func hashCookieWithout(cookie string, ignored ...string) string {
	sum := sha256.Sum256([]byte(normalizeCookieForHashWithout(cookie, ignored...)))
	return hex.EncodeToString(sum[:])
}

func normalizeCookieForHash(cookie string) string {
	return normalizeCookieForHashWithout(cookie)
}

func normalizeCookieForHashWithout(cookie string, ignored ...string) string {
	fields := parseCookieFields(cookie)
	if len(fields) == 0 {
		return strings.TrimSpace(cookie)
	}
	ignoredFields := make(map[string]struct{}, len(ignored))
	for _, field := range ignored {
		ignoredFields[field] = struct{}{}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if _, ok := ignoredFields[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(fields[key])
		builder.WriteByte(';')
	}
	return builder.String()
}

func mergeCookieField(cookie, name, value string) (string, bool) {
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if name == "" || value == "" {
		return strings.TrimSpace(cookie), false
	}
	normalized := strings.NewReplacer("\r", ";", "\n", ";").Replace(cookie)
	parts := strings.Split(normalized, ";")
	out := make([]string, 0, len(parts)+1)
	found := false
	changed := false
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(item), "cookie:") {
			item = strings.TrimSpace(item[len("cookie:"):])
		}
		index := strings.Index(item, "=")
		if index <= 0 {
			out = append(out, item)
			continue
		}
		key := strings.TrimSpace(item[:index])
		currentValue := strings.TrimSpace(item[index+1:])
		if key == name {
			if found {
				changed = true
				continue
			}
			found = true
			if currentValue != value {
				changed = true
			}
			out = append(out, name+"="+value)
			continue
		}
		out = append(out, key+"="+currentValue)
	}
	if !found {
		out = append(out, name+"="+value)
		changed = true
	}
	return strings.Join(out, "; "), changed
}

func cookieWithoutFields(cookie string, names ...string) string {
	ignored := make(map[string]struct{}, len(names))
	for _, name := range names {
		ignored[strings.TrimSpace(name)] = struct{}{}
	}
	normalized := strings.NewReplacer("\r", ";", "\n", ";").Replace(cookie)
	parts := strings.Split(normalized, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(item), "cookie:") {
			item = strings.TrimSpace(item[len("cookie:"):])
		}
		index := strings.Index(item, "=")
		if index <= 0 {
			out = append(out, item)
			continue
		}
		key := strings.TrimSpace(item[:index])
		if _, ok := ignored[key]; ok {
			continue
		}
		out = append(out, key+"="+strings.TrimSpace(item[index+1:]))
	}
	return strings.Join(out, "; ")
}

func parseCookieFields(cookie string) map[string]string {
	fields := make(map[string]string)
	normalized := strings.NewReplacer("\r", ";", "\n", ";").Replace(cookie)
	for _, part := range strings.Split(normalized, ";") {
		item := strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(item), "cookie:") {
			item = strings.TrimSpace(item[len("cookie:"):])
		}
		index := strings.Index(item, "=")
		if index <= 0 {
			continue
		}
		name := strings.TrimSpace(item[:index])
		value := strings.TrimSpace(item[index+1:])
		if name != "" && value != "" {
			fields[name] = value
		}
	}
	return fields
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
