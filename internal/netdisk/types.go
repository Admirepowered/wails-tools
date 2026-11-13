package netdisk

import "time"

type Provider string

const (
	ProviderBaidu Provider = "baidu"
	ProviderQuark Provider = "quark"
	ProviderUC    Provider = "uc"
)

type AccountStatus string

const (
	AccountStatusActive AccountStatus = "active"
	AccountStatusError  AccountStatus = "error"
)

type Account struct {
	ID           string        `json:"id"`
	Provider     Provider      `json:"provider"`
	DisplayName  string        `json:"display_name"`
	UK           string        `json:"uk,omitempty"`
	Remark       string        `json:"remark,omitempty"`
	AvatarURL    string        `json:"avatar_url,omitempty"`
	MemberType   string        `json:"member_type,omitempty"`
	UsedBytes    int64         `json:"used_bytes"`
	TotalBytes   int64         `json:"total_bytes"`
	Status       AccountStatus `json:"status"`
	Error        string        `json:"error,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	LastSyncAt   *time.Time    `json:"last_sync_at,omitempty"`
	CookieHint   string        `json:"cookie_hint,omitempty"`
	CookieHash   string        `json:"cookie_hash,omitempty"`
	IdentityHash string        `json:"identity_hash,omitempty"`
	Capabilities []string      `json:"capabilities"`
}

type AddAccountRequest struct {
	Provider    Provider `json:"provider"`
	Cookie      string   `json:"cookie"`
	DisplayName string   `json:"display_name,omitempty"`
}

type RefreshAccountRequest struct {
	AccountID string `json:"account_id"`
}

type ListFilesRequest struct {
	AccountID string `json:"account_id"`
	Path      string `json:"path,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"page_size,omitempty"`
}

type SearchFilesRequest struct {
	AccountID string `json:"account_id"`
	Path      string `json:"path,omitempty"`
	Query     string `json:"query"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"page_size,omitempty"`
}

type ResolveDownloadRequest struct {
	AccountID           string `json:"account_id"`
	FileID              string `json:"file_id"`
	Name                string `json:"name"`
	Path                string `json:"path"`
	Size                int64  `json:"size"`
	MD5                 string `json:"md5,omitempty"`
	TokenAccelerate     bool   `json:"token_accelerate,omitempty"`
	DisableCloudResolve bool   `json:"disable_cloud_resolve,omitempty"`
}

type VideoPreviewRequest struct {
	AccountID string `json:"account_id"`
	FileID    string `json:"file_id"`
	Path      string `json:"path,omitempty"`
	Name      string `json:"name,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

type VideoPreviewQuality struct {
	Key         string            `json:"key"`
	Label       string            `json:"label"`
	Resolution  string            `json:"resolution,omitempty"`
	URL         string            `json:"url"`
	Filename    string            `json:"filename,omitempty"`
	Size        int64             `json:"size,omitempty"`
	Format      string            `json:"format,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Width       int               `json:"width,omitempty"`
	Height      int               `json:"height,omitempty"`
	Bitrate     float64           `json:"bitrate,omitempty"`
	Original    bool              `json:"original,omitempty"`
	Accessable  bool              `json:"accessable,omitempty"`
	Right       string            `json:"right,omitempty"`
	UserAgent   string            `json:"user_agent,omitempty"`
	Cookie      string            `json:"cookie,omitempty"`
	Referer     string            `json:"referer,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

type VideoPreviewResult struct {
	Filename       string                `json:"filename,omitempty"`
	Size           int64                 `json:"size,omitempty"`
	DefaultQuality string                `json:"default_quality,omitempty"`
	Qualities      []VideoPreviewQuality `json:"qualities"`
}

type CreateFolderRequest struct {
	AccountID string `json:"account_id"`
	ParentID  string `json:"parent_id,omitempty"`
	Path      string `json:"path,omitempty"`
	Name      string `json:"name"`
}

type RenameFileRequest struct {
	AccountID string `json:"account_id"`
	FileID    string `json:"file_id"`
	Path      string `json:"path,omitempty"`
	NewName   string `json:"new_name"`
}

type DeleteFilesRequest struct {
	AccountID string   `json:"account_id"`
	FileIDs   []string `json:"file_ids,omitempty"`
	Paths     []string `json:"paths,omitempty"`
}

type FileOperationItem struct {
	FileID string `json:"file_id,omitempty"`
	Path   string `json:"path,omitempty"`
	Name   string `json:"name,omitempty"`
}

type FileTransferRequest struct {
	AccountID      string              `json:"account_id"`
	Files          []FileOperationItem `json:"files"`
	TargetPath     string              `json:"target_path,omitempty"`
	TargetParentID string              `json:"target_parent_id,omitempty"`
}

type UploadFileRequest struct {
	AccountID      string `json:"account_id"`
	LocalPath      string `json:"local_path"`
	Name           string `json:"name,omitempty"`
	TargetPath     string `json:"target_path,omitempty"`
	TargetParentID string `json:"target_parent_id,omitempty"`
	Overwrite      bool   `json:"overwrite,omitempty"`
}

type UploadResult struct {
	FileID string `json:"file_id,omitempty"`
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	Size   int64  `json:"size"`
}

type CreateShareRequest struct {
	OperationID string   `json:"operation_id,omitempty"`
	AccountID   string   `json:"account_id"`
	FileIDs     []string `json:"file_ids"`
	Title       string   `json:"title,omitempty"`
	Password    string   `json:"password,omitempty"`
	ExpireDays  int      `json:"expire_days,omitempty"`
}

type ShareResult struct {
	ShareID    string `json:"share_id,omitempty"`
	URL        string `json:"url"`
	Password   string `json:"password,omitempty"`
	ExpireDays int    `json:"expire_days,omitempty"`
	Title      string `json:"title,omitempty"`
}

type ListAccountSharesRequest struct {
	AccountID string `json:"account_id"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"page_size,omitempty"`
}

type CancelSharesRequest struct {
	AccountID string   `json:"account_id"`
	ShareIDs  []string `json:"share_ids"`
}

type AccountShareItem struct {
	ID        string    `json:"id"`
	Provider  Provider  `json:"provider"`
	AccountID string    `json:"account_id"`
	ShareID   string    `json:"share_id"`
	Title     string    `json:"title"`
	URL       string    `json:"url,omitempty"`
	Password  string    `json:"password,omitempty"`
	FileCount int       `json:"file_count,omitempty"`
	ViewCount int       `json:"view_count,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	ExpiredAt time.Time `json:"expired_at,omitempty"`
	Raw       any       `json:"raw,omitempty"`
}

type AccountShareList struct {
	Account Account            `json:"account"`
	Shares  []AccountShareItem `json:"shares"`
	HasMore bool               `json:"has_more"`
	Next    PageHint           `json:"next"`
}

type ListRecycleRequest struct {
	AccountID string `json:"account_id"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"page_size,omitempty"`
}

// RecycleMutationRequest carries provider-owned recycle record identifiers.
// ItemIDs must come from RecycleItem.OperationID; they are intentionally
// opaque because Baidu operates on fs_id while Quark and UC operate on the
// recycle record_id rather than the original file fid.
type RecycleMutationRequest struct {
	AccountID string   `json:"account_id"`
	ItemIDs   []string `json:"item_ids"`
}

type RecycleItem struct {
	ID          string    `json:"id"`
	OperationID string    `json:"operation_id,omitempty"`
	Provider    Provider  `json:"provider"`
	AccountID   string    `json:"account_id"`
	Name        string    `json:"name"`
	Path        string    `json:"path,omitempty"`
	ParentID    string    `json:"parent_id,omitempty"`
	IsDir       bool      `json:"is_dir"`
	Size        int64     `json:"size"`
	MD5         string    `json:"md5,omitempty"`
	DeletedAt   time.Time `json:"deleted_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	Raw         any       `json:"raw,omitempty"`
}

type RecycleList struct {
	Account Account       `json:"account"`
	Files   []RecycleItem `json:"files"`
	HasMore bool          `json:"has_more"`
	Next    PageHint      `json:"next"`
}

type DownloadLink struct {
	URL       string            `json:"url"`
	Filename  string            `json:"filename"`
	Size      int64             `json:"size"`
	UserAgent string            `json:"user_agent,omitempty"`
	Cookie    string            `json:"cookie,omitempty"`
	Referer   string            `json:"referer,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Policy    string            `json:"policy,omitempty"`
	CacheHit  bool              `json:"cache_hit,omitempty"`
}

type File struct {
	ID         string    `json:"id"`
	Provider   Provider  `json:"provider"`
	AccountID  string    `json:"account_id"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	ParentID   string    `json:"parent_id,omitempty"`
	IsDir      bool      `json:"is_dir"`
	Size       int64     `json:"size"`
	MD5        string    `json:"md5,omitempty"`
	Thumbnail  string    `json:"thumbnail_url,omitempty"`
	PreviewURL string    `json:"preview_url,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	Raw        any       `json:"raw,omitempty"`
}

type FileList struct {
	Account Account  `json:"account"`
	Path    string   `json:"path"`
	Files   []File   `json:"files"`
	HasMore bool     `json:"has_more"`
	Next    PageHint `json:"next"`
}

type PageHint struct {
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"page_size,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
}
