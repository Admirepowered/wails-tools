package netdisk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"wailstools/internal/applog"
)

type BaiduClient struct {
	http *http.Client
}

type baiduLoginStatus struct {
	Errno     int             `json:"errno"`
	UID       any             `json:"uid"`
	UK        any             `json:"uk"`
	UserID    any             `json:"user_id"`
	Username  string          `json:"username"`
	UName     string          `json:"uname"`
	PhotoURL  string          `json:"photo_url"`
	AvatarURL string          `json:"avatar_url"`
	VIPType   any             `json:"vip_type"`
	VIPLevel  any             `json:"vip_level"`
	VIPID     any             `json:"vip_identity"`
	LoginInfo baiduProfile    `json:"login_info"`
	UserInfo  baiduProfile    `json:"user_info"`
	User      json.RawMessage `json:"user"`
}

type baiduProfile struct {
	UID          any    `json:"uid"`
	UK           any    `json:"uk"`
	UserID       any    `json:"user_id"`
	Username     string `json:"username"`
	UName        string `json:"uname"`
	Name         string `json:"name"`
	Nickname     string `json:"nickname"`
	DisplayName  string `json:"display_name"`
	DisplayName2 string `json:"displayname"`
	PhotoURL     string `json:"photo_url"`
	AvatarURL    string `json:"avatar_url"`
	Avatar       string `json:"avatar"`
	Portrait     string `json:"portrait"`
	VIPType      any    `json:"vip_type"`
	VIPLevel     any    `json:"vip_level"`
	VIPID        any    `json:"vip_identity"`
}

type baiduFileItem struct {
	FSID           any               `json:"fs_id"`
	ServerFilename string            `json:"server_filename"`
	IsDir          int               `json:"isdir"`
	Size           int64             `json:"size"`
	ServerMTime    int64             `json:"server_mtime"`
	Path           string            `json:"path"`
	MD5            string            `json:"md5"`
	Thumbs         map[string]string `json:"thumbs"`
	Thumbnail      string            `json:"thumbnail"`
	ThumbnailURL   string            `json:"thumbnail_url"`
	PreviewURL     string            `json:"preview_url"`
	CoverURL       string            `json:"cover_url"`
}

func NewBaiduClient(client *http.Client) *BaiduClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &BaiduClient{http: client}
}

func baiduAPIError(operation string, errno int, messages ...string) error {
	message := firstNonEmpty(messages...)
	if message == "" {
		message = strconv.Itoa(errno)
	}
	if errno == -6 {
		return fmt.Errorf("%w: baidu %s errno %d %s", ErrProviderUnauthorized, operation, errno, message)
	}
	return fmt.Errorf("baidu %s failed: %s", operation, message)
}

func (c *BaiduClient) Probe(ctx context.Context, cookie string) (Account, error) {
	var login baiduLoginStatus
	if err := c.getJSON(ctx, "https://pan.baidu.com/api/loginStatus?clienttype=1&app_id=250528&web=1&channel=web&version=0", cookie, &login); err != nil {
		return Account{}, err
	}
	if login.Errno != 0 {
		return Account{}, ErrProviderUnauthorized
	}
	var quota struct {
		Errno int   `json:"errno"`
		Used  int64 `json:"used"`
		Total int64 `json:"total"`
	}
	_ = c.getJSON(ctx, "https://pan.baidu.com/api/quota?clienttype=8&app_id=250528&web=1", cookie, &quota)
	identity := firstNonEmptyMeaningful(login.identity(), baiduCookieIdentity(cookie))
	return Account{
		Provider:     ProviderBaidu,
		DisplayName:  firstNonEmpty(login.displayName(), "Baidu Netdisk"),
		UK:           login.userKey(),
		AvatarURL:    login.avatarURL(),
		MemberType:   login.memberType(),
		IdentityHash: identityHash(ProviderBaidu, identity),
		UsedBytes:    quota.Used,
		TotalBytes:   quota.Total,
		Capabilities: capabilities(ProviderBaidu),
	}, nil
}

func (s baiduLoginStatus) userKey() string {
	user := s.userProfile()
	return firstNonEmptyMeaningful(
		stringifyID(s.UK),
		stringifyID(s.LoginInfo.UK),
		stringifyID(s.UserInfo.UK),
		stringifyID(user.UK),
		stringifyID(s.UID),
		stringifyID(s.LoginInfo.UID),
		stringifyID(s.UserInfo.UID),
		stringifyID(user.UID),
	)
}

func (s baiduLoginStatus) identity() string {
	user := s.userProfile()
	return firstNonEmptyMeaningful(
		stringifyID(s.UID),
		stringifyID(s.UK),
		stringifyID(s.UserID),
		stringifyID(s.LoginInfo.UID),
		stringifyID(s.LoginInfo.UK),
		stringifyID(s.LoginInfo.UserID),
		stringifyID(s.UserInfo.UID),
		stringifyID(s.UserInfo.UK),
		stringifyID(s.UserInfo.UserID),
		stringifyID(user.UID),
		stringifyID(user.UK),
		stringifyID(user.UserID),
	)
}

func baiduCookieIdentity(cookie string) string {
	fields := parseCookieFields(cookie)
	return firstNonEmptyMeaningful(fields["USERID"], fields["SAVEUSERID"])
}

func (s baiduLoginStatus) displayName() string {
	user := s.userProfile()
	return firstNonEmpty(
		s.LoginInfo.displayName(),
		s.UserInfo.displayName(),
		user.displayName(),
		s.Username,
		s.UName,
	)
}

func (s baiduLoginStatus) avatarURL() string {
	user := s.userProfile()
	return firstNonEmpty(
		s.LoginInfo.avatarURL(),
		s.UserInfo.avatarURL(),
		user.avatarURL(),
		s.PhotoURL,
		s.AvatarURL,
	)
}

func (s baiduLoginStatus) userProfile() baiduProfile {
	if len(s.User) == 0 {
		return baiduProfile{}
	}
	var profile baiduProfile
	_ = json.Unmarshal(s.User, &profile)
	return profile
}

func (p baiduProfile) displayName() string {
	return firstNonEmpty(p.Username, p.UName, p.Nickname, p.DisplayName, p.DisplayName2, p.Name)
}

func (p baiduProfile) avatarURL() string {
	return firstNonEmpty(p.PhotoURL, p.AvatarURL, p.Avatar, p.Portrait)
}

func (s baiduLoginStatus) memberType() string {
	user := s.userProfile()
	if baiduIsSVIP(s.VIPType, s.VIPID) ||
		baiduIsSVIP(s.LoginInfo.VIPType, s.LoginInfo.VIPID) ||
		baiduIsSVIP(s.UserInfo.VIPType, s.UserInfo.VIPID) ||
		baiduIsSVIP(user.VIPType, user.VIPID) {
		return "SVIP"
	}
	if baiduIsVIP(s.VIPType, s.VIPID) ||
		baiduIsVIP(s.LoginInfo.VIPType, s.LoginInfo.VIPID) ||
		baiduIsVIP(s.UserInfo.VIPType, s.UserInfo.VIPID) ||
		baiduIsVIP(user.VIPType, user.VIPID) {
		return "VIP"
	}
	return "NORMAL"
}

func baiduIsSVIP(vipType, vipID any) bool {
	return numericValue(vipType) == 2 || numericValue(vipID)/10 == 2
}

func baiduIsVIP(vipType, vipID any) bool {
	return numericValue(vipType) > 0 || numericValue(vipID) > 0
}

func (c *BaiduClient) ListFiles(ctx context.Context, account Account, cookie string, req ListFilesRequest) (FileList, error) {
	path := req.Path
	if strings.TrimSpace(path) == "" {
		path = "/"
	}
	endpoint := "https://pan.baidu.com/api/list?clienttype=0&app_id=250528&web=1&num=1000&order=name&desc=0&dir=" + url.QueryEscape(path)
	var payload struct {
		Errno int             `json:"errno"`
		List  []baiduFileItem `json:"list"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return FileList{}, err
	}
	if payload.Errno != 0 {
		return FileList{}, baiduAPIError("list", payload.Errno, fmt.Sprintf("errno %d", payload.Errno))
	}
	files := mapBaiduFiles(account, payload.List)
	return FileList{Account: account, Path: path, Files: files}, nil
}

func (c *BaiduClient) SearchFiles(ctx context.Context, account Account, cookie string, req SearchFilesRequest) (FileList, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return FileList{}, errors.New("search query is required")
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = "/"
	}
	page := req.Page
	if page < 1 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize < 1 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	params := url.Values{
		"clienttype": {"0"},
		"app_id":     {"250528"},
		"web":        {"1"},
		"recursion":  {"1"},
		"key":        {query},
		"dir":        {path},
		"page":       {strconv.Itoa(page)},
		"num":        {strconv.Itoa(pageSize)},
	}
	var payload struct {
		Errno   int             `json:"errno"`
		HasMore int             `json:"has_more"`
		List    []baiduFileItem `json:"list"`
	}
	if err := c.getJSON(ctx, "https://pan.baidu.com/api/search?"+params.Encode(), cookie, &payload); err != nil {
		return FileList{}, err
	}
	if payload.Errno != 0 {
		return FileList{}, baiduAPIError("search", payload.Errno, fmt.Sprintf("errno %d", payload.Errno))
	}
	hasMore := payload.HasMore == 1 || len(payload.List) >= pageSize
	next := PageHint{}
	if hasMore {
		next = PageHint{Page: page + 1, PageSize: pageSize}
	}
	return FileList{
		Account: account,
		Path:    path,
		Files:   mapBaiduFiles(account, payload.List),
		HasMore: hasMore,
		Next:    next,
	}, nil
}

func mapBaiduFiles(account Account, items []baiduFileItem) []File {
	files := make([]File, 0, len(items))
	for _, item := range items {
		thumbnail := firstNonEmpty(
			thumbnailFromMap(item.Thumbs, "url3", "url2", "url1", "icon", "thumb"),
			item.ThumbnailURL,
			item.Thumbnail,
			item.PreviewURL,
			item.CoverURL,
		)
		files = append(files, File{
			ID:         stringifyID(item.FSID),
			Provider:   ProviderBaidu,
			AccountID:  account.ID,
			Name:       item.ServerFilename,
			Path:       item.Path,
			IsDir:      item.IsDir == 1,
			Size:       item.Size,
			MD5:        item.MD5,
			Thumbnail:  thumbnail,
			PreviewURL: firstNonEmpty(item.PreviewURL, thumbnail),
			UpdatedAt:  unixTime(item.ServerMTime),
			Raw:        item,
		})
	}
	return files
}

func (*BaiduClient) ResolveDownload(context.Context, Account, string, ResolveDownloadRequest) (DownloadLink, error) {
	return DownloadLink{}, errors.New("baidu downloads must be resolved through line 3")
}

func (c *BaiduClient) ResolveVideoPreview(ctx context.Context, account Account, cookie string, req VideoPreviewRequest) (VideoPreviewResult, error) {
	filePath := strings.TrimSpace(req.Path)
	if filePath == "" {
		return VideoPreviewResult{}, errors.New("path is required")
	}
	filename := firstNonEmpty(req.Name, netdiskBaseName(filePath))
	done := applog.Begin("netdisk.baidu", "resolve_video_preview", map[string]any{
		"account_id": account.ID,
		"file_id":    req.FileID,
		"name":       filename,
		"path":       filePath,
		"size":       req.Size,
	})

	var firstErr error
	type qualityResult struct {
		index   int
		option  baiduStreamingQualityOption
		quality VideoPreviewQuality
		err     error
	}
	results := make(chan qualityResult, len(baiduStreamingQualityOptions))
	resolveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for index, option := range baiduStreamingQualityOptions {
		go func(index int, option baiduStreamingQualityOption) {
			qualityCtx, cancelQuality := context.WithTimeout(resolveCtx, baiduStreamingQualityTimeout)
			defer cancelQuality()
			quality, err := c.resolveBaiduStreamingQuality(qualityCtx, cookie, filePath, filename, req.Size, option)
			results <- qualityResult{index: index, option: option, quality: quality, err: err}
		}(index, option)
	}

	ordered := make([]VideoPreviewQuality, len(baiduStreamingQualityOptions))
	for range baiduStreamingQualityOptions {
		result := <-results
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			applog.Warn("netdisk.baidu", "streaming_quality_skipped", result.err.Error(), map[string]any{
				"account_id": account.ID,
				"type":       result.option.Type,
				"label":      result.option.Label,
			})
			continue
		}
		ordered[result.index] = result.quality
	}
	qualities := make([]VideoPreviewQuality, 0, len(ordered))
	for _, quality := range ordered {
		if quality.Key != "" {
			qualities = append(qualities, quality)
		}
	}

	if len(qualities) == 0 {
		if firstErr == nil {
			firstErr = errors.New("baidu video preview did not return playable qualities")
		}
		done(firstErr, map[string]any{"quality_count": 0})
		return VideoPreviewResult{}, firstErr
	}

	result := VideoPreviewResult{
		Filename:       filename,
		Size:           req.Size,
		DefaultQuality: baiduDefaultVideoQuality(qualities),
		Qualities:      qualities,
	}
	done(nil, map[string]any{
		"filename":        result.Filename,
		"size":            result.Size,
		"default_quality": result.DefaultQuality,
		"quality_count":   len(result.Qualities),
	})
	return result, nil
}

type baiduStreamingQualityOption struct {
	Key        string
	Label      string
	Resolution string
	Type       string
}

var baiduStreamingQualityOptions = []baiduStreamingQualityOption{
	{Key: "baidu-m3u8-1080", Label: "超清", Resolution: "1080", Type: "M3U8_AUTO_1080"},
	{Key: "baidu-m3u8-720", Label: "高清", Resolution: "720", Type: "M3U8_AUTO_720"},
	{Key: "baidu-m3u8-480", Label: "流畅", Resolution: "480", Type: "M3U8_AUTO_480"},
}

const baiduStreamingQualityTimeout = 7 * time.Second

func (c *BaiduClient) resolveBaiduStreamingQuality(ctx context.Context, cookie, filePath, filename string, size int64, option baiduStreamingQualityOption) (VideoPreviewQuality, error) {
	endpoint := baiduStreamingEndpoint(filePath, option.Type, "")
	finalURL, contentType, err := c.resolveBaiduStreamingURL(ctx, cookie, endpoint)
	if err != nil {
		return VideoPreviewQuality{}, err
	}
	headers := map[string]string{
		"User-Agent": baiduStreamingUserAgent,
		"Referer":    "https://pan.baidu.com/",
		"Cookie":     cookie,
		"Accept":     "application/vnd.apple.mpegurl, application/x-mpegURL, text/plain, */*",
	}
	return VideoPreviewQuality{
		Key:         option.Key,
		Label:       option.Label,
		Resolution:  option.Resolution,
		URL:         finalURL,
		Filename:    filename,
		Size:        size,
		Format:      "hls",
		ContentType: firstNonEmpty(contentType, "application/vnd.apple.mpegurl"),
		Accessable:  true,
		UserAgent:   baiduStreamingUserAgent,
		Cookie:      cookie,
		Referer:     "https://pan.baidu.com/",
		Headers:     headers,
	}, nil
}

const baiduStreamingUserAgent = "pan.baidu.com"

func baiduStreamingEndpoint(filePath, streamType, adToken string) string {
	values := url.Values{}
	values.Set("app_id", "250528")
	values.Set("clienttype", "80")
	values.Set("channel", "chunlei")
	values.Set("web", "1")
	values.Set("isplayer", "1")
	values.Set("check_blue", "1")
	values.Set("path", filePath)
	values.Set("vip", "0")
	values.Set("type", streamType)
	if strings.TrimSpace(adToken) != "" {
		values.Set("adToken", adToken)
	}
	return "https://pan.baidu.com/api/streaming?" + values.Encode()
}

func (c *BaiduClient) resolveBaiduStreamingURL(ctx context.Context, cookie, endpoint string) (string, string, error) {
	body, contentType, err := c.fetchBaiduStreaming(ctx, cookie, endpoint)
	if err != nil {
		return "", contentType, err
	}
	if isM3U8Body(body) {
		return endpoint, contentType, nil
	}

	var payload struct {
		Errno    int    `json:"errno"`
		Error    string `json:"error_msg"`
		Errmsg   string `json:"errmsg"`
		ShowMsg  string `json:"show_msg"`
		AdToken  string `json:"adToken"`
		Request  any    `json:"request_id"`
		Request2 any    `json:"requestId"`
	}
	trimmed := bytes.TrimSpace(body)
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return "", contentType, errors.New("baidu streaming did not return m3u8")
	}
	if payload.Errno == 133 && strings.TrimSpace(payload.AdToken) != "" {
		retryEndpoint, err := addBaiduStreamingAdToken(endpoint, payload.AdToken)
		if err != nil {
			return "", contentType, err
		}
		body, contentType, err = c.fetchBaiduStreaming(ctx, cookie, retryEndpoint)
		if err != nil {
			return "", contentType, err
		}
		if isM3U8Body(body) {
			return retryEndpoint, contentType, nil
		}
		return "", contentType, errors.New("baidu streaming ad-token retry did not return m3u8")
	}
	if payload.Errno != 0 {
		return "", contentType, baiduAPIError("streaming", payload.Errno, payload.Error, payload.Errmsg, payload.ShowMsg, strconv.Itoa(payload.Errno))
	}
	return "", contentType, errors.New("baidu streaming did not return m3u8")
}

func (c *BaiduClient) fetchBaiduStreaming(ctx context.Context, cookie, endpoint string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", baiduStreamingUserAgent)
	req.Header.Set("Accept", "application/vnd.apple.mpegurl, application/x-mpegURL, text/plain, */*")
	req.Header.Set("Referer", "https://pan.baidu.com/")
	req.Header.Set("Cookie", cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, contentType, ErrProviderUnauthorized
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		return nil, contentType, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, contentType, fmt.Errorf("baidu streaming request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, contentType, nil
}

func addBaiduStreamingAdToken(endpoint, adToken string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	values := parsed.Query()
	values.Set("adToken", adToken)
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func isM3U8Body(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(body), []byte("#EXTM3U"))
}

func baiduDefaultVideoQuality(qualities []VideoPreviewQuality) string {
	for _, preferred := range []string{"baidu-m3u8-1080", "baidu-m3u8-720", "baidu-m3u8-480"} {
		for _, quality := range qualities {
			if quality.Key == preferred {
				return quality.Key
			}
		}
	}
	if len(qualities) > 0 {
		return qualities[0].Key
	}
	return ""
}

func (c *BaiduClient) CreateFolder(ctx context.Context, account Account, cookie string, req CreateFolderRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return errors.New("folder name is required")
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = "/"
	}
	fullPath := buildChildPath(path, name)
	endpoint := "https://pcs.baidu.com/rest/2.0/pcs/file?app_id=778750&method=mkdir&path=" + url.QueryEscape(fullPath)
	var payload struct {
		Errno int    `json:"errno"`
		Error string `json:"error_msg"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return err
	}
	if payload.Errno != 0 {
		return baiduAPIError("create folder", payload.Errno, payload.Error, strconv.Itoa(payload.Errno))
	}
	return nil
}

func (c *BaiduClient) RenameFile(ctx context.Context, account Account, cookie string, req RenameFileRequest) error {
	if strings.TrimSpace(req.FileID) == "" {
		return errors.New("file id is required")
	}
	if strings.TrimSpace(req.Path) == "" {
		return errors.New("path is required")
	}
	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		return errors.New("new name is required")
	}
	fileID, err := strconv.ParseInt(req.FileID, 10, 64)
	if err != nil {
		return err
	}
	filelist, err := json.Marshal([]map[string]any{{
		"id":      fileID,
		"path":    req.Path,
		"newname": newName,
		"ondup":   "fail",
	}})
	if err != nil {
		return err
	}
	return c.baiduFileManager(ctx, cookie, "rename", "fail", string(filelist))
}

func (c *BaiduClient) DeleteFiles(ctx context.Context, account Account, cookie string, req DeleteFilesRequest) error {
	paths := cleanPaths(req.Paths)
	if len(paths) == 0 {
		return errors.New("paths are required")
	}
	if err := c.baiduDeleteFileManager(ctx, cookie, paths); err == nil {
		return nil
	} else {
		fileManagerErr := err
		if err := c.baiduDeletePCS(ctx, cookie, paths); err == nil {
			return nil
		} else if errors.Is(fileManagerErr, ErrProviderUnauthorized) {
			return fileManagerErr
		} else if errors.Is(err, ErrProviderUnauthorized) {
			return err
		} else {
			return fmt.Errorf("baidu delete failed: %v; fallback failed: %v", fileManagerErr, err)
		}
	}
}

func (c *BaiduClient) baiduDeleteFileManager(ctx context.Context, cookie string, paths []string) error {
	filelist, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	return c.baiduFileManager(ctx, cookie, "delete", "fail", string(filelist))
}

func (c *BaiduClient) CopyFiles(ctx context.Context, account Account, cookie string, req FileTransferRequest) error {
	filelist, err := baiduTransferFileList(req)
	if err != nil {
		return err
	}
	return c.baiduFileManager(ctx, cookie, "copy", "newcopy", filelist)
}

func (c *BaiduClient) MoveFiles(ctx context.Context, account Account, cookie string, req FileTransferRequest) error {
	filelist, err := baiduTransferFileList(req)
	if err != nil {
		return err
	}
	return c.baiduFileManager(ctx, cookie, "move", "newcopy", filelist)
}

func (c *BaiduClient) CreateShare(ctx context.Context, account Account, cookie string, req CreateShareRequest) (ShareResult, error) {
	fids, err := baiduShareFileIDs(req.FileIDs)
	if err != nil {
		return ShareResult{}, err
	}
	expireDays := req.ExpireDays
	if expireDays <= 0 {
		expireDays = 365
	}
	password := strings.TrimSpace(req.Password)
	values := url.Values{}
	values.Set("is_knowledge", "0")
	values.Set("public", "0")
	values.Set("period", strconv.Itoa(expireDays))
	values.Set("pwd", password)
	values.Set("eflag_disable", "true")
	values.Set("linkOrQrcode", "link")
	values.Set("channel_list", "[]")
	values.Set("schannel", "4")
	values.Set("fid_list", fids)
	endpoint := "https://pan.baidu.com/share/pset?channel=chunlei&clienttype=0&app_id=250528&web=1"
	var out struct {
		Errno    int    `json:"errno"`
		Error    string `json:"error_msg"`
		Errmsg   string `json:"errmsg"`
		ShowMsg  string `json:"show_msg"`
		ShortURL string `json:"shorturl"`
		URL      string `json:"url"`
		Link     string `json:"link"`
		ShareID  any    `json:"shareid"`
	}
	if err := c.postFormJSON(ctx, endpoint, cookie, values, &out); err != nil {
		return ShareResult{}, err
	}
	if out.Errno != 0 {
		return ShareResult{}, baiduAPIError("share", out.Errno, out.Error, out.Errmsg, out.ShowMsg, strconv.Itoa(out.Errno))
	}
	shareURL := firstNonEmpty(out.ShortURL, out.URL, out.Link)
	if shareURL == "" {
		return ShareResult{}, errors.New("baidu did not return a share url")
	}
	return ShareResult{
		ShareID:    stringifyID(out.ShareID),
		URL:        shareURL,
		Password:   password,
		ExpireDays: expireDays,
		Title:      req.Title,
	}, nil
}

func (c *BaiduClient) ListAccountShares(ctx context.Context, account Account, cookie string, req ListAccountSharesRequest) (AccountShareList, error) {
	page, pageSize := normalizeListPage(req.Page, req.PageSize, 100)
	values := url.Values{}
	values.Set("channel", "chunlei")
	values.Set("clienttype", "0")
	values.Set("app_id", "250528")
	values.Set("web", "1")
	values.Set("order", "ctime")
	values.Set("desc", "1")
	values.Set("is_batch", "1")
	values.Set("page", strconv.Itoa(page))
	values.Set("num", strconv.Itoa(pageSize))
	endpoint := "https://pan.baidu.com/share/record?" + values.Encode()
	var payload struct {
		Errno      int               `json:"errno"`
		Error      string            `json:"error_msg"`
		Errmsg     string            `json:"errmsg"`
		ShowMsg    string            `json:"show_msg"`
		List       []json.RawMessage `json:"list"`
		Total      int               `json:"total"`
		TotalCount int               `json:"total_count"`
		Count      int               `json:"count"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return AccountShareList{}, err
	}
	if payload.Errno != 0 {
		return AccountShareList{}, baiduAPIError("share list", payload.Errno, payload.Error, payload.Errmsg, payload.ShowMsg, strconv.Itoa(payload.Errno))
	}
	shares := make([]AccountShareItem, 0, len(payload.List))
	for _, rawItem := range payload.List {
		rawMap := rawMapFromJSON(rawItem)
		shareID := firstNonEmpty(stringField(rawMap, "shareId"), stringField(rawMap, "shareid"), stringField(rawMap, "share_id"))
		typicalPath := firstNonEmpty(stringField(rawMap, "typicalPath"), stringField(rawMap, "typical_path"))
		title := firstNonEmpty(
			stringField(rawMap, "title"),
			stringField(rawMap, "name"),
			stringField(rawMap, "server_filename"),
			netdiskBaseName(typicalPath),
			"分享_"+shareID,
		)
		title = firstNonEmptyMeaningful(
			netdiskBaseName(firstNonEmpty(typicalPath, stringField(rawMap, "path"))),
			stringField(rawMap, "server_filename"),
			stringField(rawMap, "filename"),
			stringField(rawMap, "file_name"),
			stringField(rawMap, "typicalTitle"),
			stringField(rawMap, "typical_title"),
			stringField(rawMap, "name"),
			stringField(rawMap, "title"),
			title,
		)
		shareURL := normalizeBaiduShareURL(firstNonEmpty(
			stringField(rawMap, "shortlink"),
			stringField(rawMap, "shortLink"),
			stringField(rawMap, "shorturl"),
			stringField(rawMap, "shortUrl"),
			stringField(rawMap, "url"),
			stringField(rawMap, "link"),
		))
		fileCount := firstPositiveInt(
			int(numericValue(rawMap["file_count"])),
			int(numericValue(rawMap["fileCount"])),
			int(numericValue(rawMap["fileCnt"])),
			int(numericValue(rawMap["file_num"])),
			rawListLength(rawMap["fsIds"]),
			rawListLength(rawMap["fs_ids"]),
			rawListLength(rawMap["fid_list"]),
		)
		viewCount := firstPositiveInt(
			int(numericValue(rawMap["vCnt"])),
			int(numericValue(rawMap["viewCount"])),
			int(numericValue(rawMap["view_count"])),
			int(numericValue(rawMap["visit_count"])),
		)
		shares = append(shares, AccountShareItem{
			ID:        firstNonEmpty(shareID, title),
			Provider:  ProviderBaidu,
			AccountID: account.ID,
			ShareID:   shareID,
			Title:     title,
			URL:       shareURL,
			Password:  firstNonEmpty(stringField(rawMap, "passwd"), stringField(rawMap, "password"), stringField(rawMap, "pwd")),
			FileCount: fileCount,
			ViewCount: viewCount,
			CreatedAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["ctime"]),
				numericValue(rawMap["create_time"]),
				numericValue(rawMap["created_at"]),
			)),
			ExpiredAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["expiredTime"]),
				numericValue(rawMap["expired_time"]),
				numericValue(rawMap["expired_at"]),
			)),
			Raw: rawMap,
		})
		shares[len(shares)-1].ExpiredAt = baiduShareExpiredTime(firstNonZeroInt64(
			numericValue(rawMap["expiredTime"]),
			numericValue(rawMap["expired_time"]),
			numericValue(rawMap["expired_at"]),
			numericValue(rawMap["expire_time"]),
		), rawMap)
	}
	total := firstPositiveInt(payload.TotalCount, payload.Total, payload.Count)
	return AccountShareList{
		Account: account,
		Shares:  shares,
		HasMore: hasMoreListPage(total, page, pageSize, len(shares)),
		Next:    PageHint{Page: page + 1, PageSize: pageSize},
	}, nil
}

func (c *BaiduClient) CancelShares(ctx context.Context, account Account, cookie string, req CancelSharesRequest) error {
	shareIDs := cleanIDs(req.ShareIDs)
	if len(shareIDs) == 0 {
		return errors.New("share ids are required")
	}
	values := url.Values{}
	values.Set("shareid_list", mustJSONList(shareIDs))
	endpoint := "https://pan.baidu.com/share/cancel?channel=chunlei&clienttype=0&app_id=250528&web=1"
	var out struct {
		Errno   int    `json:"errno"`
		Error   string `json:"error_msg"`
		Errmsg  string `json:"errmsg"`
		ShowMsg string `json:"show_msg"`
	}
	if err := c.postFormJSON(ctx, endpoint, cookie, values, &out); err != nil {
		return err
	}
	if out.Errno != 0 {
		return baiduAPIError("cancel share", out.Errno, out.Error, out.Errmsg, out.ShowMsg, strconv.Itoa(out.Errno))
	}
	return nil
}

func (c *BaiduClient) ListRecycle(ctx context.Context, account Account, cookie string, req ListRecycleRequest) (RecycleList, error) {
	page, pageSize := normalizeListPage(req.Page, req.PageSize, 100)
	values := url.Values{}
	values.Set("clienttype", "8")
	values.Set("app_id", "250528")
	values.Set("web", "1")
	values.Set("page", strconv.Itoa(page))
	values.Set("num", strconv.Itoa(pageSize))
	endpoint := "https://pan.baidu.com/api/recycle/list?" + values.Encode()
	var payload struct {
		Errno      int               `json:"errno"`
		Error      string            `json:"error_msg"`
		Errmsg     string            `json:"errmsg"`
		ShowMsg    string            `json:"show_msg"`
		List       []json.RawMessage `json:"list"`
		Total      int               `json:"total"`
		TotalCount int               `json:"total_count"`
		Count      int               `json:"count"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return RecycleList{}, err
	}
	if payload.Errno != 0 {
		return RecycleList{}, baiduAPIError("recycle list", payload.Errno, payload.Error, payload.Errmsg, payload.ShowMsg, strconv.Itoa(payload.Errno))
	}
	files := make([]RecycleItem, 0, len(payload.List))
	for _, rawItem := range payload.List {
		rawMap := rawMapFromJSON(rawItem)
		id := firstNonEmpty(stringField(rawMap, "fs_id"), stringField(rawMap, "fsid"), stringField(rawMap, "id"))
		name := firstNonEmpty(stringField(rawMap, "server_filename"), stringField(rawMap, "filename"), stringField(rawMap, "name"), netdiskBaseName(stringField(rawMap, "path")))
		files = append(files, RecycleItem{
			ID:          firstNonEmpty(id, stringField(rawMap, "path"), name),
			OperationID: id,
			Provider:    ProviderBaidu,
			AccountID:   account.ID,
			Name:        name,
			Path:        stringField(rawMap, "path"),
			IsDir:       boolField(firstNonZeroAny(rawMap["isdir"], rawMap["is_dir"], rawMap["dir"])),
			Size:        numericValue(rawMap["size"]),
			MD5:         stringField(rawMap, "md5"),
			DeletedAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["delete_time"]),
				numericValue(rawMap["deleteTime"]),
				numericValue(rawMap["dtime"]),
				numericValue(rawMap["oper_time"]),
			)),
			UpdatedAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["server_mtime"]),
				numericValue(rawMap["local_mtime"]),
				numericValue(rawMap["mtime"]),
			)),
			Raw: rawMap,
		})
	}
	total := firstPositiveInt(payload.TotalCount, payload.Total, payload.Count)
	return RecycleList{
		Account: account,
		Files:   files,
		HasMore: hasMoreListPage(total, page, pageSize, len(files)),
		Next:    PageHint{Page: page + 1, PageSize: pageSize},
	}, nil
}

func (c *BaiduClient) RestoreRecycle(ctx context.Context, account Account, cookie string, req RecycleMutationRequest) error {
	return c.mutateRecycle(ctx, cookie, "restore", req)
}

func (c *BaiduClient) DeleteRecycle(ctx context.Context, account Account, cookie string, req RecycleMutationRequest) error {
	return c.mutateRecycle(ctx, cookie, "delete", req)
}

func (c *BaiduClient) mutateRecycle(ctx context.Context, cookie, operation string, req RecycleMutationRequest) error {
	fidList, err := baiduFIDList(req.ItemIDs)
	if err != nil {
		return err
	}
	values := url.Values{}
	values.Set("fidlist", fidList)
	query := url.Values{}
	query.Set("clienttype", "0")
	query.Set("app_id", "250528")
	query.Set("web", "1")
	query.Set("async", "0")
	endpoint := "https://pan.baidu.com/api/recycle/" + operation + "?" + query.Encode()
	var out struct {
		Errno   int    `json:"errno"`
		Error   string `json:"error_msg"`
		Errmsg  string `json:"errmsg"`
		ShowMsg string `json:"show_msg"`
	}
	if err := c.postFormJSON(ctx, endpoint, cookie, values, &out); err != nil {
		return err
	}
	if out.Errno != 0 {
		return baiduAPIError("recycle "+operation, out.Errno, out.Error, out.Errmsg, out.ShowMsg, strconv.Itoa(out.Errno))
	}
	return nil
}

func (c *BaiduClient) baiduDeletePCS(ctx context.Context, cookie string, paths []string) error {
	payload, err := baiduDeleteParam(paths)
	if err != nil {
		return err
	}
	values := url.Values{}
	values.Set("app_id", "778750")
	values.Set("method", "delete")
	values.Set("param", payload)
	endpoint := "https://pcs.baidu.com/rest/2.0/pcs/file?" + values.Encode()
	var out struct {
		Errno     int    `json:"errno"`
		ErrorCode int    `json:"error_code"`
		Error     string `json:"error_msg"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &out); err != nil {
		return err
	}
	if out.Errno != 0 || out.ErrorCode != 0 {
		errno := out.Errno
		if errno == 0 {
			errno = out.ErrorCode
		}
		return baiduAPIError("delete", errno, out.Error, strconv.Itoa(errno))
	}
	return nil
}

func baiduDeleteParam(paths []string) (string, error) {
	items := make([]map[string]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" {
			items = append(items, map[string]string{"path": path})
		}
	}
	if len(items) == 0 {
		return "", errors.New("paths are required")
	}
	payload, err := json.Marshal(map[string]any{"list": items})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func baiduShareFileIDs(fileIDs []string) (string, error) {
	items := make([]any, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		fileID = strings.TrimSpace(fileID)
		if fileID == "" {
			continue
		}
		if parsed, err := strconv.ParseInt(fileID, 10, 64); err == nil {
			items = append(items, parsed)
		} else {
			items = append(items, fileID)
		}
	}
	if len(items) == 0 {
		return "", errors.New("file ids are required")
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func baiduTransferFileList(req FileTransferRequest) (string, error) {
	targetPath := strings.TrimSpace(req.TargetPath)
	if targetPath == "" {
		targetPath = "/"
	}
	items := make([]map[string]any, 0, len(req.Files))
	for _, file := range req.Files {
		filePath := strings.TrimSpace(file.Path)
		if filePath == "" {
			return "", errors.New("file path is required")
		}
		fileName := firstNonEmpty(file.Name, netdiskBaseName(filePath))
		if fileName == "" {
			return "", errors.New("file name is required")
		}
		items = append(items, map[string]any{
			"path":    filePath,
			"dest":    targetPath,
			"newname": fileName,
			"ondup":   "newcopy",
		})
	}
	if len(items) == 0 {
		return "", errors.New("files are required")
	}
	payload, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func cleanPaths(paths []string) []string {
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" {
			cleaned = append(cleaned, path)
		}
	}
	return cleaned
}

func (c *BaiduClient) baiduFileManager(ctx context.Context, cookie, opera, onnest, filelist string) error {
	values := url.Values{}
	values.Set("filelist", filelist)
	query := url.Values{}
	query.Set("async", "0")
	query.Set("onnest", onnest)
	query.Set("opera", opera)
	query.Set("clienttype", "0")
	query.Set("app_id", "250528")
	query.Set("web", "1")
	endpoint := "https://pan.baidu.com/api/filemanager?" + query.Encode()
	var out struct {
		Errno   int    `json:"errno"`
		Error   string `json:"error_msg"`
		Errmsg  string `json:"errmsg"`
		ShowMsg string `json:"show_msg"`
	}
	if err := c.postFormJSON(ctx, endpoint, cookie, values, &out); err != nil {
		return err
	}
	if out.Errno != 0 {
		return baiduAPIError(opera, out.Errno, out.Error, out.Errmsg, out.ShowMsg, strconv.Itoa(out.Errno))
	}
	return nil
}

func (c *BaiduClient) getJSON(ctx context.Context, endpoint, cookie string, out any) error {
	done := applog.Begin("netdisk.baidu", "http_get", map[string]any{"url": endpoint})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		done(err, nil)
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Cookie", cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		done(err, nil)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{"status": resp.StatusCode})
		return ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		err := fmt.Errorf("baidu request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
		done(err, map[string]any{"status": resp.StatusCode})
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(out)
	done(err, map[string]any{"status": resp.StatusCode})
	return err
}

func (c *BaiduClient) postFormJSON(ctx context.Context, endpoint, cookie string, values url.Values, out any) error {
	done := applog.Begin("netdisk.baidu", "http_post_form", map[string]any{"url": endpoint})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		done(err, nil)
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://pan.baidu.com/")
	req.Header.Set("Cookie", cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		done(err, nil)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{"status": resp.StatusCode})
		return ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		err := fmt.Errorf("baidu request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
		done(err, map[string]any{"status": resp.StatusCode})
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(out)
	done(err, map[string]any{"status": resp.StatusCode})
	return err
}

type QuarkClient struct {
	http *http.Client
}

const quarkPCUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/3.24.0 Chrome/112.0.5615.165 Electron/24.1.3.8 Safari/537.36 Channel/pckk_other_ch"

type quarkAccountInfoResponse struct {
	Success bool      `json:"success"`
	Code    string    `json:"code"`
	Data    quarkUser `json:"data"`
}

type quarkUser struct {
	Nickname  string `json:"nickname"`
	NickName  string `json:"nick_name"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	UID       any    `json:"uid"`
	UserID    any    `json:"user_id"`
	MobileKPS string `json:"mobilekps"`
	KPS       string `json:"kps"`
	AvatarURI string `json:"avatarUri"`
	AvatarURL string `json:"avatar_url"`
	Avatar    string `json:"avatar"`
	PhotoURL  string `json:"photo_url"`
}

func (u quarkUser) displayName() string {
	return firstNonEmpty(u.Nickname, u.NickName, u.Username, u.Name)
}

func (u quarkUser) avatarURL() string {
	return firstNonEmpty(u.AvatarURI, u.AvatarURL, u.Avatar, u.PhotoURL)
}

func (u quarkUser) identity() string {
	return firstNonEmpty(u.MobileKPS, u.Username, stringifyID(u.UID), u.KPS, stringifyID(u.UserID))
}

func (u quarkUser) ucIdentity() string {
	return firstNonEmpty(stringifyID(u.UID), stringifyID(u.UserID), u.MobileKPS, u.KPS, u.Username)
}

type quarkCapacity struct {
	Used       int64
	Total      int64
	MemberType string
}

func NewQuarkClient(client *http.Client) *QuarkClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &QuarkClient{http: client}
}

func (c *QuarkClient) Probe(ctx context.Context, cookie string) (Account, error) {
	account := Account{
		Provider:     ProviderQuark,
		DisplayName:  "Quark Cloud Drive",
		Capabilities: capabilities(ProviderQuark),
	}

	if user, err := c.quarkAccountInfo(ctx, cookie); err == nil {
		account.DisplayName = firstNonEmpty(user.displayName(), account.DisplayName)
		account.AvatarURL = user.avatarURL()
		account.IdentityHash = identityHash(ProviderQuark, user.identity())
	}
	if capacity, err := c.quarkCapacity(ctx, cookie); err == nil {
		account.UsedBytes = capacity.Used
		account.TotalBytes = capacity.Total
		account.MemberType = capacity.MemberType
	}
	if _, err := c.ListFiles(ctx, Account{ID: "probe", Provider: ProviderQuark}, cookie, ListFilesRequest{ParentID: "0", PageSize: 1}); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (c *QuarkClient) quarkAccountInfo(ctx context.Context, cookie string) (quarkUser, error) {
	var payload quarkAccountInfoResponse
	if err := c.getJSON(ctx, "https://pan.quark.cn/account/info?fr=pc&platform=pc", cookie, &payload); err != nil {
		return quarkUser{}, err
	}
	if !payload.Success && !strings.EqualFold(payload.Code, "OK") {
		return quarkUser{}, ErrProviderUnauthorized
	}
	return payload.Data, nil
}

func (c *QuarkClient) quarkCapacity(ctx context.Context, cookie string) (quarkCapacity, error) {
	endpoints := []string{
		"https://drive-pc.quark.cn/1/clouddrive/member?pr=ucpro&fr=pc&uc_param_str=&fetch_subscribe=true&_ch=home&fetch_identity=true",
		"https://drive-pc.quark.cn/1/clouddrive/capacity/growth/info?pr=ucpro&fr=pc&uc_param_str=",
		quarkMobileEndpoint("https://drive-m.quark.cn/1/clouddrive/capacity/growth/info", cookie),
		"https://drive-pc.quark.cn/1/clouddrive/capacity?pr=ucpro&fr=pc&uc_param_str=",
	}
	var lastErr error
	for _, endpoint := range endpoints {
		var payload struct {
			Status  int             `json:"status"`
			Code    any             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
			lastErr = err
			continue
		}
		if payload.Status != 0 && payload.Status != 200 {
			lastErr = fmt.Errorf("quark capacity failed: %s", firstNonEmpty(payload.Message, stringifyID(payload.Status), stringifyID(payload.Code)))
			continue
		}
		capacity := parseQuarkCapacity(payload.Data)
		if capacity.Used > 0 || capacity.Total > 0 {
			return capacity, nil
		}
	}
	if lastErr != nil {
		return quarkCapacity{}, lastErr
	}
	return quarkCapacity{}, errors.New("quark capacity is missing")
}

func quarkMobileEndpoint(baseURL, cookie string) string {
	values := url.Values{}
	values.Set("pr", "ucpro")
	values.Set("fr", "android")
	fields := parseCookieFields(cookie)
	for _, key := range []string{"kps", "sign", "vcode"} {
		if value := strings.TrimSpace(fields[key]); value != "" {
			values.Set(key, strings.ReplaceAll(value, "%25", "%"))
		}
	}
	return baseURL + "?" + values.Encode()
}

func parseQuarkCapacity(data json.RawMessage) quarkCapacity {
	if len(data) == 0 || string(data) == "null" {
		return quarkCapacity{}
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return quarkCapacity{}
	}
	flattened := make(map[string]int64)
	collectNumberFields("", root, flattened)
	used := firstNumberField(flattened,
		"use_capacity",
		"used_capacity",
		"used",
		"usage",
		"used_size",
		"used_bytes",
		"drive_used_size",
		"total_use_capacity",
		"secret_use_capacity",
	)
	total := firstNumberField(flattened,
		"total_capacity",
		"capacity",
		"total",
		"total_size",
		"total_bytes",
		"drive_capacity",
		"secret_total_capacity",
	)
	if used <= 0 {
		used = sumNumberFields(flattened, "use_capacity", "used_capacity", "used_size")
	}
	if total <= 0 {
		total = sumNumberFields(flattened, "total_capacity", "capacity", "total_size")
	}
	return quarkCapacity{Used: used, Total: total, MemberType: quarkMemberType(root)}
}

func quarkMemberType(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"member_type", "memberType", "vip_type", "vipType"} {
			if raw, ok := typed[key]; ok {
				if memberType := strings.TrimSpace(stringifyID(raw)); memberType != "" && memberType != "<nil>" {
					return memberType
				}
			}
		}
		for _, key := range []string{"member_status", "member_status_map", "subscribe_status_map"} {
			if status, ok := typed[key].(map[string]any); ok {
				for _, memberKey := range []string{"SUPER_VIP", "SVIP", "SV"} {
					if raw, ok := status[memberKey]; ok && isQuarkMemberStatusActive(raw) {
						return memberKey
					}
				}
			}
		}
		for _, item := range typed {
			if memberType := quarkMemberType(item); memberType != "" {
				return memberType
			}
		}
	case []any:
		for _, item := range typed {
			if memberType := quarkMemberType(item); memberType != "" {
				return memberType
			}
		}
	}
	return ""
}

func isQuarkMemberStatusActive(value any) bool {
	status := strings.TrimSpace(stringifyID(value))
	return status != "" && status != "<nil>" && !strings.EqualFold(status, "UNPAID")
}

func collectNumberFields(prefix string, value any, out map[string]int64) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			collectNumberFields(normalizedKey, item, out)
		}
	case []any:
		for _, item := range typed {
			collectNumberFields(prefix, item, out)
		}
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
			out[prefix] += int64(typed)
		}
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
			out[prefix] += parsed
		}
	}
}

func firstNumberField(values map[string]int64, keys ...string) int64 {
	for _, key := range keys {
		if value := values[strings.ToLower(key)]; value > 0 {
			return value
		}
	}
	return 0
}

func sumNumberFields(values map[string]int64, keys ...string) int64 {
	var total int64
	for _, key := range keys {
		total += values[strings.ToLower(key)]
	}
	return total
}

func (c *QuarkClient) ListFiles(ctx context.Context, account Account, cookie string, req ListFilesRequest) (FileList, error) {
	parentID := strings.TrimSpace(req.ParentID)
	if parentID == "" {
		parentID = "0"
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	endpoint := fmt.Sprintf("https://drive-pc.quark.cn/1/clouddrive/file/sort?pr=ucpro&fr=pc&uc_param_str=&pdir_fid=%s&_page=%d&_size=%d&_fetch_total=1&_fetch_sub_dirs=0&fetch_dir_file_num=n&last_view_file_size=1&recent_file_size=3&check_series_dir=1&use_custom_sort=y&mix_scene=cloud_list_home&_sort=file_type:asc,updated_at:desc", url.QueryEscape(parentID), page, pageSize)
	var payload quarkFileListPayload
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return FileList{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return FileList{}, fmt.Errorf("quark list failed: %s", firstNonEmpty(payload.Message, strconv.Itoa(payload.Status)))
	}
	files := mapQuarkFiles(account, ProviderQuark, payload.Data.List, normalizeQuarkPreviewURL)
	next := PageHint{Page: page + 1, PageSize: pageSize, ParentID: parentID}
	return FileList{Account: account, Path: req.Path, Files: files, HasMore: quarkFileListHasMore(payload, page, pageSize), Next: next}, nil
}

func (c *QuarkClient) SearchFiles(ctx context.Context, account Account, cookie string, req SearchFilesRequest) (FileList, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return FileList{Account: account, Path: "/", Files: []File{}}, nil
	}
	page, pageSize := normalizeListPage(req.Page, req.PageSize, 100)
	values := url.Values{}
	values.Set("pr", "ucpro")
	values.Set("fr", "pc")
	values.Set("uc_param_str", "")
	values.Set("q", query)
	values.Set("_page", strconv.Itoa(page))
	values.Set("_size", strconv.Itoa(pageSize))
	values.Set("_fetch_total", "1")
	values.Set("_sort", "file_type:asc,updated_at:desc")
	values.Set("_is_hl", "1")
	endpoint := "https://drive-pc.quark.cn/1/clouddrive/file/search?" + values.Encode()
	var payload quarkFileListPayload
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return FileList{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return FileList{}, fmt.Errorf("quark search failed: %s", firstNonEmpty(payload.Message, strconv.Itoa(payload.Status)))
	}
	files := mapQuarkFiles(account, ProviderQuark, payload.Data.List, normalizeQuarkPreviewURL)
	return FileList{
		Account: account,
		Path:    "/",
		Files:   files,
		HasMore: quarkFileListHasMore(payload, page, pageSize),
		Next:    PageHint{Page: page + 1, PageSize: pageSize},
	}, nil
}

type UCClient struct {
	http *http.Client
}

const ucPCUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) UCBrowser/13.0 Safari/537.36"

type ucQuota struct {
	Used       int64
	Total      int64
	MemberType string
}

func NewUCClient(client *http.Client) *UCClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &UCClient{http: client}
}

func (c *UCClient) Probe(ctx context.Context, cookie string) (Account, error) {
	account := Account{
		Provider:     ProviderUC,
		DisplayName:  "UC Cloud Drive",
		Capabilities: capabilities(ProviderUC),
	}
	if user, err := c.ucAccountInfo(ctx, cookie); err == nil {
		account.DisplayName = firstNonEmpty(user.displayName(), account.DisplayName)
		account.AvatarURL = user.avatarURL()
		account.IdentityHash = identityHash(ProviderUC, user.ucIdentity())
	}
	if quota, err := c.ucQuota(ctx, cookie); err == nil {
		account.UsedBytes = quota.Used
		account.TotalBytes = quota.Total
		account.MemberType = quota.MemberType
	} else {
		return Account{}, err
	}
	if account.IdentityHash == "" {
		fields := parseCookieFields(cookie)
		account.IdentityHash = identityHash(ProviderUC, firstNonEmpty(fields["__pus"], fields["kps"], fields["ucid"], fields["uid"]))
	}
	if _, err := c.ListFiles(ctx, Account{ID: "probe", Provider: ProviderUC}, cookie, ListFilesRequest{ParentID: "0", PageSize: 1}); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (c *UCClient) ucAccountInfo(ctx context.Context, cookie string) (quarkUser, error) {
	var payload quarkAccountInfoResponse
	if err := c.getJSON(ctx, "https://drive.uc.cn/account/info?fr=pc&platform=pc", cookie, &payload); err != nil {
		return quarkUser{}, err
	}
	if !payload.Success && !strings.EqualFold(payload.Code, "OK") {
		return quarkUser{}, ErrProviderUnauthorized
	}
	return payload.Data, nil
}

func ucQuarkValues() url.Values {
	values := url.Values{}
	values.Set("pr", "UCBrowser")
	values.Set("fr", "pc")
	values.Set("uc_param_str", "")
	return values
}

func ucQuarkEndpoint(path string, extra url.Values) string {
	values := ucQuarkValues()
	for key, list := range extra {
		values.Del(key)
		for _, value := range list {
			values.Add(key, value)
		}
	}
	return "https://drive-pc.quark.cn" + "/" + strings.TrimLeft(path, "/") + "?" + values.Encode()
}

func ucQuarkDriveEndpoint(path string, extra url.Values) string {
	values := ucQuarkValues()
	for key, list := range extra {
		values.Del(key)
		for _, value := range list {
			values.Add(key, value)
		}
	}
	return "https://drive.quark.cn" + "/" + strings.TrimLeft(path, "/") + "?" + values.Encode()
}

func ucPCApiEndpoint(path string, extra url.Values) string {
	values := ucQuarkValues()
	for key, list := range extra {
		values.Del(key)
		for _, value := range list {
			values.Add(key, value)
		}
	}
	return "https://pc-api.uc.cn" + "/" + strings.TrimLeft(path, "/") + "?" + values.Encode()
}

func (c *UCClient) ucQuota(ctx context.Context, cookie string) (ucQuota, error) {
	values := url.Values{}
	values.Set("pr", "UCBrowser")
	values.Set("fr", "pc")
	values.Set("fetch_subscribe", "true")
	values.Set("_ch", "home")
	endpoint := "https://pc-api.uc.cn/1/clouddrive/member?" + values.Encode()
	var payload struct {
		Status  int             `json:"status"`
		Code    any             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return ucQuota{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return ucQuota{}, ucOperationError("quota", payload.Message, payload.Status, payload.Code)
	}
	capacity := parseQuarkCapacity(payload.Data)
	if capacity.Used <= 0 && capacity.Total <= 0 {
		return ucQuota{}, errors.New("获取 UC 网盘容量失败：上游未返回容量信息")
	}
	return ucQuota{Used: capacity.Used, Total: capacity.Total, MemberType: capacity.MemberType}, nil
}

func (c *UCClient) ListFiles(ctx context.Context, account Account, cookie string, req ListFilesRequest) (FileList, error) {
	parentID := strings.TrimSpace(req.ParentID)
	if parentID == "" {
		parentID = "0"
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	values := ucQuarkValues()
	values.Set("pdir_fid", parentID)
	values.Set("_page", strconv.Itoa(page))
	values.Set("_size", strconv.Itoa(pageSize))
	values.Set("_fetch_total", "1")
	values.Set("_fetch_sub_dirs", "0")
	values.Set("fetch_dir_file_num", "n")
	values.Set("last_view_file_size", "1")
	values.Set("recent_file_size", "3")
	values.Set("check_series_dir", "1")
	values.Set("use_custom_sort", "y")
	values.Set("mix_scene", "cloud_list_home")
	values.Set("_sort", "file_type:asc,updated_at:desc")
	endpoint := ucQuarkEndpoint("/1/clouddrive/file/sort", values)
	var payload quarkFileListPayload
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return FileList{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return FileList{}, ucOperationError("list", payload.Message, payload.Status, payload.Code)
	}
	files := mapQuarkFiles(account, ProviderUC, payload.Data.List, normalizeUCPreviewURL)
	next := PageHint{Page: page + 1, PageSize: pageSize, ParentID: parentID}
	return FileList{Account: account, Path: req.Path, Files: files, HasMore: quarkFileListHasMore(payload, page, pageSize), Next: next}, nil
}

func (c *UCClient) SearchFiles(ctx context.Context, account Account, cookie string, req SearchFilesRequest) (FileList, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return FileList{Account: account, Path: "/", Files: []File{}}, nil
	}
	page, pageSize := normalizeListPage(req.Page, req.PageSize, 100)
	values := url.Values{}
	values.Set("q", query)
	values.Set("_page", strconv.Itoa(page))
	values.Set("_size", strconv.Itoa(pageSize))
	values.Set("_fetch_total", "1")
	values.Set("_sort", "file_type:asc,updated_at:desc")
	values.Set("_is_hl", "1")
	endpoint := ucQuarkEndpoint("/1/clouddrive/file/search", values)
	var payload quarkFileListPayload
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return FileList{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return FileList{}, ucOperationError("search", payload.Message, payload.Status, payload.Code)
	}
	files := mapQuarkFiles(account, ProviderUC, payload.Data.List, normalizeUCPreviewURL)
	return FileList{
		Account: account,
		Path:    "/",
		Files:   files,
		HasMore: quarkFileListHasMore(payload, page, pageSize),
		Next:    PageHint{Page: page + 1, PageSize: pageSize},
	}, nil
}

func (c *UCClient) ResolveDownload(ctx context.Context, account Account, cookie string, req ResolveDownloadRequest) (DownloadLink, error) {
	if strings.TrimSpace(req.FileID) == "" {
		return DownloadLink{}, errors.New("获取 UC 网盘下载链接失败：文件 ID 不能为空")
	}
	bodyMap := map[string]any{
		"fids": []string{req.FileID},
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return DownloadLink{}, err
	}
	endpoint := ucQuarkEndpoint("/1/clouddrive/file/download", nil)
	done := applog.Begin("netdisk.uc", "resolve_download", map[string]any{
		"account_id":                 account.ID,
		"file_id":                    req.FileID,
		"name":                       req.Name,
		"size":                       req.Size,
		"url":                        endpoint,
		"token_accelerate":           false,
		"token_accelerate_requested": req.TokenAccelerate,
		"request_cookie":             providerCookieDebugFields(cookie),
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		done(err, nil)
		return DownloadLink{}, err
	}
	httpReq.Header.Set("User-Agent", ucPCUserAgent)
	httpReq.Header.Set("Accept", "application/json, text/plain, */*")
	httpReq.Header.Set("Content-Type", "application/json;charset=utf-8")
	httpReq.Header.Set("Referer", "https://drive.uc.cn/")
	httpReq.Header.Set("Origin", "https://drive.uc.cn")
	requestCookie := quarkCookieWithCapturedResponseFields(ctx, cookie)
	httpReq.Header.Set("Cookie", requestCookie)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		done(err, nil)
		return DownloadLink{}, err
	}
	defer resp.Body.Close()
	captureQuarkResponseCookies(ctx, resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{
			"status":           resp.StatusCode,
			"body":             providerBodySnippet(resp),
			"request_cookie":   providerCookieDebugFields(requestCookie),
			"set_cookie_names": responseCookieNames(resp),
		})
		return DownloadLink{}, ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := providerBodySnippet(resp)
		err := ucOperationError("download", detail, resp.StatusCode, resp.StatusCode)
		done(err, map[string]any{"status": resp.StatusCode, "body": detail, "request_cookie": providerCookieDebugFields(requestCookie), "set_cookie_names": responseCookieNames(resp)})
		return DownloadLink{}, err
	}
	downloadCookie := collectUCDownloadCookie(resp, requestCookie)
	var payload struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    []struct {
			FID         string `json:"fid"`
			FileName    string `json:"file_name"`
			Size        int64  `json:"size"`
			DownloadURL string `json:"download_url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		done(err, map[string]any{"status": resp.StatusCode})
		return DownloadLink{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		err := ucOperationError("download", payload.Message, payload.Status, payload.Code)
		done(err, map[string]any{"status": resp.StatusCode, "upstream_status": payload.Status, "upstream_code": payload.Code, "upstream_message": payload.Message, "request_cookie": providerCookieDebugFields(requestCookie), "set_cookie_names": responseCookieNames(resp)})
		return DownloadLink{}, err
	}
	if len(payload.Data) == 0 || strings.TrimSpace(payload.Data[0].DownloadURL) == "" {
		err := errors.New("获取 UC 网盘下载链接失败：上游未返回下载链接")
		done(err, map[string]any{"status": resp.StatusCode, "upstream_status": payload.Status, "upstream_code": payload.Code, "upstream_message": payload.Message, "request_cookie": providerCookieDebugFields(requestCookie), "set_cookie_names": responseCookieNames(resp)})
		return DownloadLink{}, err
	}
	if downloadCookie == "" {
		err := errors.New("获取 UC 网盘下载链接失败：下载凭证缺失，请重新登录 UC 网盘后再试")
		done(err, map[string]any{"status": resp.StatusCode, "upstream_status": payload.Status, "upstream_code": payload.Code, "upstream_message": payload.Message, "request_cookie": providerCookieDebugFields(requestCookie), "download_cookie": providerCookieDebugFields(downloadCookie), "set_cookie_names": responseCookieNames(resp)})
		return DownloadLink{}, err
	}
	filename := firstNonEmpty(payload.Data[0].FileName, req.Name)
	size := payload.Data[0].Size
	if size <= 0 {
		size = req.Size
	}
	done(nil, map[string]any{
		"status":                     resp.StatusCode,
		"upstream_code":              payload.Code,
		"filename":                   filename,
		"size":                       size,
		"download_url":               payload.Data[0].DownloadURL,
		"download_cookie":            providerCookieDebugFields(downloadCookie),
		"set_cookie_names":           responseCookieNames(resp),
		"token_accelerate":           false,
		"token_accelerate_requested": req.TokenAccelerate,
	})
	referer := ucDownloadReferer(payload.Data[0].DownloadURL)
	headers := map[string]string{
		"User-Agent": ucPCUserAgent,
		"Cookie":     downloadCookie,
	}
	if referer != "" {
		headers["Referer"] = referer
	}
	return DownloadLink{
		URL:       payload.Data[0].DownloadURL,
		Filename:  filename,
		Size:      size,
		UserAgent: ucPCUserAgent,
		Cookie:    downloadCookie,
		Referer:   referer,
		Headers:   headers,
	}, nil
}

func (c *UCClient) ResolveVideoPreview(ctx context.Context, account Account, cookie string, req VideoPreviewRequest) (VideoPreviewResult, error) {
	if strings.TrimSpace(req.FileID) == "" {
		return VideoPreviewResult{}, errors.New("获取 UC 网盘视频播放地址失败：文件 ID 不能为空")
	}
	endpoint := ucPCApiEndpoint("/1/clouddrive/file/v2/play", nil)
	body := map[string]any{
		"fid":         req.FileID,
		"resolutions": "normal,low,high,super,2k,4k",
		"supports":    "fmp4,m3u8",
	}
	if quarkShouldUseFreeLimit(account) {
		body["use_right"] = "free_limit"
	}
	done := applog.Begin("netdisk.uc", "resolve_video_preview", map[string]any{
		"account_id": account.ID,
		"file_id":    req.FileID,
		"name":       req.Name,
		"free_limit": quarkShouldUseFreeLimit(account),
		"url":        endpoint,
	})
	var payload quarkVideoPlayResponse
	if err := c.postJSON(ctx, endpoint, cookie, body, &payload); err != nil {
		done(err, nil)
		return VideoPreviewResult{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		err := ucOperationError("video_preview", payload.Message, payload.Status, payload.Code)
		done(err, map[string]any{"status": payload.Status, "code": payload.Code})
		return VideoPreviewResult{}, err
	}
	playbackCookie := quarkCookieWithCapturedResponseFields(ctx, cookie)
	result := ucVideoPreviewResult(req, payload, playbackCookie)
	if len(result.Qualities) == 0 {
		err := errors.New("获取 UC 网盘视频播放地址失败：没有可播放的清晰度")
		done(err, map[string]any{"status": payload.Status, "code": payload.Code})
		return VideoPreviewResult{}, err
	}
	done(nil, map[string]any{
		"filename":        result.Filename,
		"size":            result.Size,
		"default_quality": result.DefaultQuality,
		"quality_count":   len(result.Qualities),
	})
	return result, nil
}

func (c *UCClient) CreateFolder(ctx context.Context, account Account, cookie string, req CreateFolderRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return errors.New("新建 UC 网盘文件夹失败：文件夹名称不能为空")
	}
	parentID := strings.TrimSpace(req.ParentID)
	if parentID == "" {
		parentID = "0"
	}
	body := map[string]any{
		"pdir_fid":      parentID,
		"file_name":     name,
		"dir_init_lock": false,
		"dir_path":      "",
	}
	var out quarkStatusResponse
	if err := c.postJSON(ctx, ucQuarkEndpoint("/1/clouddrive/file", nil), cookie, body, &out); err != nil {
		return err
	}
	return ucStatusError(out, "create_folder")
}

func (c *UCClient) RenameFile(ctx context.Context, account Account, cookie string, req RenameFileRequest) error {
	if strings.TrimSpace(req.FileID) == "" {
		return errors.New("重命名 UC 网盘文件失败：文件 ID 不能为空")
	}
	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		return errors.New("重命名 UC 网盘文件失败：新名称不能为空")
	}
	body := map[string]any{
		"fid":       req.FileID,
		"file_name": newName,
	}
	var out quarkStatusResponse
	if err := c.postJSON(ctx, ucQuarkEndpoint("/1/clouddrive/file/rename", nil), cookie, body, &out); err != nil {
		return err
	}
	return ucStatusError(out, "rename")
}

func (c *UCClient) DeleteFiles(ctx context.Context, account Account, cookie string, req DeleteFilesRequest) error {
	if len(req.FileIDs) == 0 {
		return errors.New("删除 UC 网盘文件失败：请选择要删除的文件")
	}
	body := map[string]any{
		"action_type":  2,
		"filelist":     req.FileIDs,
		"exclude_fids": []string{},
	}
	var out quarkStatusResponse
	if err := c.postJSON(ctx, ucQuarkEndpoint("/1/clouddrive/file/delete", nil), cookie, body, &out); err != nil {
		return err
	}
	return ucStatusError(out, "delete")
}

func (c *UCClient) CopyFiles(ctx context.Context, account Account, cookie string, req FileTransferRequest) error {
	return c.transferFiles(ctx, cookie, "copy", req)
}

func (c *UCClient) MoveFiles(ctx context.Context, account Account, cookie string, req FileTransferRequest) error {
	return c.transferFiles(ctx, cookie, "move", req)
}

func (c *UCClient) CreateShare(ctx context.Context, account Account, cookie string, req CreateShareRequest) (ShareResult, error) {
	fileIDs := cleanIDs(req.FileIDs)
	if len(fileIDs) == 0 {
		return ShareResult{}, errors.New("创建 UC 网盘分享失败：请选择要分享的文件")
	}
	expireDays := req.ExpireDays
	if expireDays < 0 {
		expireDays = 0
	}
	password := strings.TrimSpace(req.Password)
	body := map[string]any{
		"fid_list":     fileIDs,
		"title":        strings.TrimSpace(req.Title),
		"url_type":     1,
		"expired_type": 1,
	}
	if password != "" {
		body["url_type"] = 2
		body["passcode"] = password
	}
	if expireDays > 0 {
		body["expired_type"] = 2
		body["expired_at"] = time.Now().Add(time.Duration(expireDays) * 24 * time.Hour).UnixMilli()
	}
	var out quarkTransferResponse
	if err := c.postJSON(ctx, ucQuarkEndpoint("/1/clouddrive/share", nil), cookie, body, &out); err != nil {
		return ShareResult{}, err
	}
	if out.Status != 0 && out.Status != 200 {
		return ShareResult{}, ucOperationError("share", out.Message, out.Status, out.Code)
	}
	shareID := out.Data.ShareID
	if shareID == "" && out.Data.TaskID != "" {
		var err error
		shareID, err = c.waitUCShareTask(ctx, cookie, out.Data.TaskID, 1000)
		if err != nil {
			return ShareResult{}, err
		}
	}
	if shareID == "" {
		return ShareResult{}, errors.New("创建 UC 网盘分享失败：上游未返回分享 ID")
	}
	detail, err := c.ucShareDetail(ctx, cookie, shareID)
	if err != nil {
		return ShareResult{}, err
	}
	detail.Password = firstNonEmpty(detail.Password, password)
	detail.ExpireDays = expireDays
	if detail.Title == "" {
		detail.Title = req.Title
	}
	return detail, nil
}

func (c *UCClient) ListAccountShares(ctx context.Context, account Account, cookie string, req ListAccountSharesRequest) (AccountShareList, error) {
	page, pageSize := normalizeListPage(req.Page, req.PageSize, 50)
	values := url.Values{}
	values.Set("pr", "UCBrowser")
	values.Set("fr", "pc")
	values.Set("uc_param_str", "")
	values.Set("_page", strconv.Itoa(page))
	values.Set("_size", strconv.Itoa(pageSize))
	values.Set("_order_field", "created_at")
	values.Set("_order_type", "desc")
	values.Set("_fetch_total", "1")
	values.Set("_fetch_notify_follow", "1")
	endpoint := "https://drive-pc.quark.cn/1/clouddrive/share/mypage/detail?" + values.Encode()
	var payload struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			List  []json.RawMessage `json:"list"`
			Total int               `json:"total"`
		} `json:"data"`
		Metadata struct {
			Total int `json:"_total"`
		} `json:"metadata"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return AccountShareList{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return AccountShareList{}, ucOperationError("share_list", payload.Message, payload.Status, payload.Code)
	}
	shares := make([]AccountShareItem, 0, len(payload.Data.List))
	for _, rawItem := range payload.Data.List {
		rawMap := rawMapFromJSON(rawItem)
		shareID := firstNonEmpty(stringField(rawMap, "share_id"), stringField(rawMap, "shareId"), stringField(rawMap, "id"))
		title := firstNonEmpty(
			stringField(rawMap, "title"),
			stringField(rawMap, "share_title"),
			stringField(rawMap, "file_name"),
			stringField(rawMap, "first_file_name"),
			stringField(rawMap, "name"),
			"分享_"+shareID,
		)
		fileCount := firstPositiveInt(
			int(numericValue(rawMap["file_count"])),
			int(numericValue(rawMap["file_num"])),
			int(numericValue(rawMap["fileNum"])),
			int(numericValue(rawMap["num"])),
			rawListLength(rawMap["fid_list"]),
			rawListLength(rawMap["filelist"]),
		)
		viewCount := firstPositiveInt(
			int(numericValue(rawMap["view_count"])),
			int(numericValue(rawMap["viewCount"])),
			int(numericValue(rawMap["visit_count"])),
			int(numericValue(rawMap["read_count"])),
		)
		shares = append(shares, AccountShareItem{
			ID:        firstNonEmpty(shareID, title),
			Provider:  ProviderUC,
			AccountID: account.ID,
			ShareID:   shareID,
			Title:     title,
			URL: normalizeUCPreviewURL(firstNonEmpty(
				stringField(rawMap, "share_url"),
				stringField(rawMap, "shareUrl"),
				stringField(rawMap, "url"),
				stringField(rawMap, "short_url"),
			)),
			Password:  firstNonEmpty(stringField(rawMap, "passcode"), stringField(rawMap, "password"), stringField(rawMap, "pwd")),
			FileCount: fileCount,
			ViewCount: viewCount,
			CreatedAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["created_at"]),
				numericValue(rawMap["create_time"]),
				numericValue(rawMap["ctime"]),
			)),
			ExpiredAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["expired_at"]),
				numericValue(rawMap["expire_time"]),
				numericValue(rawMap["expired_time"]),
			)),
			Raw: rawMap,
		})
	}
	total := firstPositiveInt(payload.Data.Total, payload.Metadata.Total)
	return AccountShareList{
		Account: account,
		Shares:  shares,
		HasMore: hasMoreListPage(total, page, pageSize, len(shares)),
		Next:    PageHint{Page: page + 1, PageSize: pageSize},
	}, nil
}

func (c *UCClient) CancelShares(ctx context.Context, account Account, cookie string, req CancelSharesRequest) error {
	shareIDs := cleanIDs(req.ShareIDs)
	if len(shareIDs) == 0 {
		return errors.New("取消 UC 网盘分享失败：请选择要取消的分享")
	}
	body := map[string]any{
		"share_ids": shareIDs,
	}
	var out quarkTransferResponse
	if err := c.postJSON(ctx, ucQuarkEndpoint("/1/clouddrive/share/delete", nil), cookie, body, &out); err != nil {
		return err
	}
	if out.Status != 0 && out.Status != 200 {
		return ucOperationError("cancel_share", out.Message, out.Status, out.Code)
	}
	if out.Data.TaskID != "" && !out.Data.Finish {
		return c.waitUCTask(ctx, cookie, out.Data.TaskID, out.Metadata.TQGap)
	}
	return nil
}

func (c *UCClient) ListRecycle(ctx context.Context, account Account, cookie string, req ListRecycleRequest) (RecycleList, error) {
	page, pageSize := normalizeListPage(req.Page, req.PageSize, 100)
	values := url.Values{}
	values.Set("pr", "UCBrowser")
	values.Set("fr", "pc")
	values.Set("uc_param_str", "")
	values.Set("_page", strconv.Itoa(page))
	values.Set("_size", strconv.Itoa(pageSize))
	values.Set("_fetch_total", "1")
	values.Set("_sort", "updated_at:desc")
	endpoint := "https://drive-pc.quark.cn/1/clouddrive/file/recycle/list?" + values.Encode()
	var payload struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			List  []json.RawMessage `json:"list"`
			Total int               `json:"total"`
		} `json:"data"`
		Metadata struct {
			Total int `json:"_total"`
			Count int `json:"_count"`
		} `json:"metadata"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return RecycleList{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return RecycleList{}, ucOperationError("recycle", payload.Message, payload.Status, payload.Code)
	}
	files := make([]RecycleItem, 0, len(payload.Data.List))
	for _, rawItem := range payload.Data.List {
		item, rawMap := decodeQuarkFileItem(rawItem)
		files = append(files, RecycleItem{
			ID:          firstNonEmpty(item.FID, stringField(rawMap, "id"), item.FileName),
			OperationID: firstNonEmpty(stringField(rawMap, "record_id"), stringField(rawMap, "recordId")),
			Provider:    ProviderUC,
			AccountID:   account.ID,
			Name:        item.FileName,
			Path:        stringField(rawMap, "path"),
			ParentID:    item.PDirFID,
			IsDir:       item.Dir,
			Size:        item.Size,
			MD5:         item.MD5,
			DeletedAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["deleted_at"]),
				numericValue(rawMap["delete_time"]),
				numericValue(rawMap["trashed_at"]),
				numericValue(rawMap["updated_at"]),
			)),
			UpdatedAt: unixTime(item.UpdatedAt),
			Raw:       rawMap,
		})
	}
	total := firstPositiveInt(payload.Data.Total, payload.Metadata.Total, payload.Metadata.Count)
	return RecycleList{
		Account: account,
		Files:   files,
		HasMore: hasMoreListPage(total, page, pageSize, len(files)),
		Next:    PageHint{Page: page + 1, PageSize: pageSize},
	}, nil
}

func (c *UCClient) RestoreRecycle(ctx context.Context, account Account, cookie string, req RecycleMutationRequest) error {
	return c.mutateRecycle(ctx, cookie, "recover", req)
}

func (c *UCClient) DeleteRecycle(ctx context.Context, account Account, cookie string, req RecycleMutationRequest) error {
	return c.mutateRecycle(ctx, cookie, "remove", req)
}

func (c *UCClient) mutateRecycle(ctx context.Context, cookie, operation string, req RecycleMutationRequest) error {
	ids := cleanIDs(req.ItemIDs)
	if len(ids) == 0 {
		return errors.New("recycle item ids are required")
	}
	var out quarkTransferResponse
	endpoint := ucQuarkEndpoint("/1/clouddrive/file/recycle/"+operation, nil)
	if err := c.postJSON(ctx, endpoint, cookie, map[string]any{"select_mode": 2, "record_list": ids}, &out); err != nil {
		return err
	}
	if out.Status != 0 && out.Status != 200 {
		return ucOperationError("recycle_"+operation, out.Message, out.Status, out.Code)
	}
	if out.Data.TaskID != "" && !out.Data.Finish {
		return c.waitUCTask(ctx, cookie, out.Data.TaskID, out.Metadata.TQGap)
	}
	return nil
}

func (c *UCClient) transferFiles(ctx context.Context, cookie, opera string, req FileTransferRequest) error {
	fileIDs, err := quarkTransferFileIDs(req)
	if err != nil {
		return err
	}
	targetParentID := strings.TrimSpace(req.TargetParentID)
	if targetParentID == "" {
		targetParentID = "0"
	}
	body := map[string]any{
		"action_type":  1,
		"to_pdir_fid":  targetParentID,
		"filelist":     fileIDs,
		"exclude_fids": []string{},
	}
	var out quarkTransferResponse
	if err := c.postJSON(ctx, ucQuarkEndpoint("/1/clouddrive/file/"+opera, nil), cookie, body, &out); err != nil {
		return err
	}
	if out.Status != 0 && out.Status != 200 {
		return ucOperationError(opera, out.Message, out.Status, out.Code)
	}
	if out.Data.TaskID != "" && !out.Data.Finish {
		return c.waitUCTask(ctx, cookie, out.Data.TaskID, out.Metadata.TQGap)
	}
	return nil
}

type quarkFileListPayload struct {
	Status   int    `json:"status"`
	Code     int    `json:"code"`
	Message  string `json:"message"`
	Metadata struct {
		Count int `json:"_count"`
		Total int `json:"_total"`
	} `json:"metadata"`
	Data struct {
		List     []json.RawMessage `json:"list"`
		Total    int               `json:"total"`
		Metadata struct {
			Count int `json:"_count"`
			Total int `json:"_total"`
		} `json:"metadata"`
	} `json:"data"`
}

func quarkFileListTotal(payload quarkFileListPayload) int {
	for _, total := range []int{payload.Data.Total, payload.Metadata.Count, payload.Metadata.Total, payload.Data.Metadata.Count, payload.Data.Metadata.Total} {
		if total > 0 {
			return total
		}
	}
	return 0
}

func quarkFileListHasMore(payload quarkFileListPayload, page, pageSize int) bool {
	if total := quarkFileListTotal(payload); total > 0 {
		return total > page*pageSize
	}
	return len(payload.Data.List) >= pageSize
}

type quarkFileItem struct {
	FID             string `json:"fid"`
	FileName        string `json:"file_name"`
	Dir             bool   `json:"dir"`
	Size            int64  `json:"size"`
	UpdatedAt       int64  `json:"updated_at"`
	MD5             string `json:"md5"`
	PDirFID         string `json:"pdir_fid"`
	Thumbnail       string `json:"thumbnail"`
	ThumbnailURL    string `json:"thumbnail_url"`
	BigThumbnail    string `json:"big_thumbnail"`
	TURL            string `json:"t_url"`
	SURL            string `json:"s_url"`
	PreviewURL      string `json:"preview_url"`
	QuickPreviewURL string `json:"quick_preview_url"`
	PURL            string `json:"p_url"`
	Preview         string `json:"preview"`
	Cover           string `json:"cover"`
	CoverURL        string `json:"cover_url"`
	ImageURL        string `json:"image_url"`
}

func decodeQuarkFileItem(rawItem json.RawMessage) (quarkFileItem, map[string]any) {
	var item quarkFileItem
	_ = json.Unmarshal(rawItem, &item)
	rawMap := make(map[string]any)
	_ = json.Unmarshal(rawItem, &rawMap)
	if item.FID == "" {
		item.FID = firstNonEmpty(stringField(rawMap, "fid"), stringField(rawMap, "file_id"))
	}
	if item.FileName == "" {
		item.FileName = firstNonEmpty(stringField(rawMap, "file_name"), stringField(rawMap, "name"))
	}
	if item.PDirFID == "" {
		item.PDirFID = stringField(rawMap, "pdir_fid")
	}
	if item.Size == 0 {
		item.Size = numericValue(rawMap["size"])
	}
	if item.UpdatedAt == 0 {
		item.UpdatedAt = numericValue(rawMap["updated_at"])
	}
	if item.MD5 == "" {
		item.MD5 = stringField(rawMap, "md5")
	}
	if rawDir, ok := rawMap["dir"]; ok {
		item.Dir = boolField(rawDir)
	}
	return item, rawMap
}

func mapQuarkFiles(account Account, provider Provider, rawItems []json.RawMessage, normalizePreviewURL func(string) string) []File {
	files := make([]File, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, rawMap := decodeQuarkFileItem(rawItem)
		thumbnailURL := firstNonEmpty(
			item.ThumbnailURL,
			item.Thumbnail,
			stringField(rawMap, "thumbnail_url"),
			stringField(rawMap, "thumbnail"),
			item.TURL,
			stringField(rawMap, "t_url"),
			item.BigThumbnail,
			stringField(rawMap, "big_thumbnail"),
			item.SURL,
			stringField(rawMap, "s_url"),
			item.CoverURL,
			item.Cover,
			item.ImageURL,
			thumbnailFromRawValue(rawMap),
		)
		previewURL := firstNonEmpty(
			item.PreviewURL,
			stringField(rawMap, "preview_url"),
			item.PURL,
			stringField(rawMap, "p_url"),
			item.QuickPreviewURL,
			stringField(rawMap, "quick_preview_url"),
			item.Preview,
			stringField(rawMap, "preview"),
		)
		files = append(files, File{
			ID:         item.FID,
			Provider:   provider,
			AccountID:  account.ID,
			Name:       item.FileName,
			Path:       firstNonEmpty(stringField(rawMap, "path"), stringField(rawMap, "file_path"), stringField(rawMap, "full_path"), stringField(rawMap, "dir_path")),
			ParentID:   item.PDirFID,
			IsDir:      item.Dir,
			Size:       item.Size,
			MD5:        item.MD5,
			Thumbnail:  normalizePreviewURL(thumbnailURL),
			PreviewURL: normalizePreviewURL(previewURL),
			UpdatedAt:  unixTime(item.UpdatedAt),
			Raw:        rawMap,
		})
	}
	return files
}

func (c *QuarkClient) ResolveDownload(ctx context.Context, account Account, cookie string, req ResolveDownloadRequest) (DownloadLink, error) {
	if strings.TrimSpace(req.FileID) == "" {
		return DownloadLink{}, errors.New("file id is required")
	}
	bodyMap := map[string]any{
		"fids": []string{req.FileID},
	}
	if req.TokenAccelerate {
		token, err := c.acquireDownloadToken(ctx, cookie)
		if err != nil {
			return DownloadLink{}, err
		}
		bodyMap["token"] = token
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return DownloadLink{}, err
	}
	endpoint := "https://drive.quark.cn/1/clouddrive/file/download?pr=ucpro&fr=pc&uc_param_str="
	done := applog.Begin("netdisk.quark", "resolve_download", map[string]any{
		"account_id":       account.ID,
		"file_id":          req.FileID,
		"name":             req.Name,
		"size":             req.Size,
		"url":              endpoint,
		"token_accelerate": req.TokenAccelerate,
		"request_cookie":   providerCookieDebugFields(cookie),
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		done(err, nil)
		return DownloadLink{}, err
	}
	httpReq.Header.Set("User-Agent", quarkPCUserAgent)
	httpReq.Header.Set("Accept", "application/json, text/plain, */*")
	httpReq.Header.Set("Content-Type", "application/json;charset=utf-8")
	httpReq.Header.Set("Referer", "https://pan.quark.cn/")
	requestCookie := quarkCookieWithCapturedResponseFields(ctx, cookie)
	httpReq.Header.Set("Cookie", requestCookie)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		done(err, nil)
		return DownloadLink{}, err
	}
	defer resp.Body.Close()
	captureQuarkResponseCookies(ctx, resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{
			"status":           resp.StatusCode,
			"body":             providerBodySnippet(resp),
			"request_cookie":   providerCookieDebugFields(requestCookie),
			"set_cookie_names": responseCookieNames(resp),
		})
		return DownloadLink{}, ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := providerBodySnippet(resp)
		err := fmt.Errorf("quark download resolve failed with status %d: %s", resp.StatusCode, detail)
		done(err, map[string]any{"status": resp.StatusCode, "body": detail, "request_cookie": providerCookieDebugFields(requestCookie), "set_cookie_names": responseCookieNames(resp)})
		return DownloadLink{}, err
	}
	downloadCookie := collectQuarkDownloadCookie(resp, requestCookie)
	var payload struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    []struct {
			FID         string `json:"fid"`
			FileName    string `json:"file_name"`
			Size        int64  `json:"size"`
			DownloadURL string `json:"download_url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		done(err, map[string]any{"status": resp.StatusCode})
		return DownloadLink{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		err := fmt.Errorf("quark download resolve failed: %s", firstNonEmpty(payload.Message, strconv.Itoa(payload.Status)))
		done(err, map[string]any{"status": resp.StatusCode, "upstream_status": payload.Status, "upstream_code": payload.Code, "upstream_message": payload.Message, "request_cookie": providerCookieDebugFields(requestCookie), "set_cookie_names": responseCookieNames(resp)})
		return DownloadLink{}, err
	}
	if len(payload.Data) == 0 || strings.TrimSpace(payload.Data[0].DownloadURL) == "" {
		err := errors.New("quark did not return a download url")
		done(err, map[string]any{"status": resp.StatusCode, "upstream_status": payload.Status, "upstream_code": payload.Code, "upstream_message": payload.Message, "request_cookie": providerCookieDebugFields(requestCookie), "set_cookie_names": responseCookieNames(resp)})
		return DownloadLink{}, err
	}
	if downloadCookie == "" {
		err := errors.New("quark download cookie is missing __puus")
		done(err, map[string]any{"status": resp.StatusCode, "upstream_status": payload.Status, "upstream_code": payload.Code, "upstream_message": payload.Message, "request_cookie": providerCookieDebugFields(requestCookie), "download_cookie": providerCookieDebugFields(downloadCookie), "set_cookie_names": responseCookieNames(resp)})
		return DownloadLink{}, err
	}
	filename := firstNonEmpty(payload.Data[0].FileName, req.Name)
	size := payload.Data[0].Size
	if size <= 0 {
		size = req.Size
	}
	userAgent := quarkPCUserAgent
	done(nil, map[string]any{
		"status":           resp.StatusCode,
		"upstream_code":    payload.Code,
		"filename":         filename,
		"size":             size,
		"download_url":     payload.Data[0].DownloadURL,
		"download_cookie":  providerCookieDebugFields(downloadCookie),
		"set_cookie_names": responseCookieNames(resp),
		"token_accelerate": req.TokenAccelerate,
	})
	return DownloadLink{
		URL:       payload.Data[0].DownloadURL,
		Filename:  filename,
		Size:      size,
		UserAgent: userAgent,
		Cookie:    downloadCookie,
		Referer:   "https://pan.quark.cn/",
		Headers: map[string]string{
			"User-Agent": userAgent,
			"Referer":    "https://pan.quark.cn/",
			"Cookie":     downloadCookie,
		},
	}, nil
}

func (c *QuarkClient) ResolveVideoPreview(ctx context.Context, account Account, cookie string, req VideoPreviewRequest) (VideoPreviewResult, error) {
	if strings.TrimSpace(req.FileID) == "" {
		return VideoPreviewResult{}, errors.New("file id is required")
	}
	endpoint := "https://drive-pc.quark.cn/1/clouddrive/file/v2/play?pr=ucpro&fr=pc&uc_param_str="
	body := map[string]any{
		"fid":         req.FileID,
		"resolutions": "normal,low,high,super,2k,4k",
		"supports":    "fmp4,m3u8",
	}
	if quarkShouldUseFreeLimit(account) {
		body["use_right"] = "free_limit"
	}
	done := applog.Begin("netdisk.quark", "resolve_video_preview", map[string]any{
		"account_id": account.ID,
		"file_id":    req.FileID,
		"name":       req.Name,
		"free_limit": quarkShouldUseFreeLimit(account),
		"url":        endpoint,
	})
	var payload quarkVideoPlayResponse
	if err := c.postJSON(ctx, endpoint, cookie, body, &payload); err != nil {
		done(err, nil)
		return VideoPreviewResult{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		err := fmt.Errorf("quark video preview failed: %s", firstNonEmpty(payload.Message, strconv.Itoa(payload.Status), strconv.Itoa(payload.Code)))
		done(err, map[string]any{"status": payload.Status, "code": payload.Code})
		return VideoPreviewResult{}, err
	}
	playbackCookie := quarkCookieWithCapturedResponseFields(ctx, cookie)
	result := quarkVideoPreviewResult(req, payload, playbackCookie)
	if len(result.Qualities) == 0 {
		err := errors.New("quark video preview did not return playable qualities")
		done(err, map[string]any{"status": payload.Status, "code": payload.Code})
		return VideoPreviewResult{}, err
	}
	done(nil, map[string]any{
		"filename":        result.Filename,
		"size":            result.Size,
		"default_quality": result.DefaultQuality,
		"quality_count":   len(result.Qualities),
	})
	return result, nil
}

type quarkVideoPlayResponse struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		DefaultResolution       string               `json:"default_resolution"`
		OriginDefaultResolution string               `json:"origin_default_resolution"`
		FileName                string               `json:"file_name"`
		Size                    int64                `json:"size"`
		VideoList               []quarkVideoPlayItem `json:"video_list"`
		Meta                    quarkVideoPlayInfo   `json:"meta"`
	} `json:"data"`
}

type quarkVideoPlayItem struct {
	Resolution     string             `json:"resolution"`
	VideoInfo      quarkVideoPlayInfo `json:"video_info"`
	Right          string             `json:"right"`
	MemberRight    string             `json:"member_right"`
	TransStatus    string             `json:"trans_status"`
	Accessable     bool               `json:"accessable"`
	SupportsFormat string             `json:"supports_format"`
}

type quarkVideoPlayInfo struct {
	Duration   float64 `json:"duration"`
	Size       int64   `json:"size"`
	Format     string  `json:"format"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Bitrate    float64 `json:"bitrate"`
	Codec      string  `json:"codec"`
	FPS        float64 `json:"fps"`
	FileFormat string  `json:"file_format"`
	URL        string  `json:"url"`
	Resolution string  `json:"resolution"`
}

func quarkShouldUseFreeLimit(account Account) bool {
	member := strings.ToUpper(strings.TrimSpace(account.MemberType))
	return member == "" || member == "NORMAL" || member == "UNPAID" || member == "FREE"
}

func quarkVideoPreviewResult(req VideoPreviewRequest, payload quarkVideoPlayResponse, cookie string) VideoPreviewResult {
	return quarkLikeVideoPreviewResult(req, payload, cookie, "quark", quarkPCUserAgent, "https://pan.quark.cn/")
}

func ucVideoPreviewResult(req VideoPreviewRequest, payload quarkVideoPlayResponse, cookie string) VideoPreviewResult {
	return quarkLikeVideoPreviewResult(req, payload, cookie, "uc", ucPCUserAgent, "https://drive.uc.cn/")
}

func quarkLikeVideoPreviewResult(req VideoPreviewRequest, payload quarkVideoPlayResponse, cookie, providerKey, userAgent, referer string) VideoPreviewResult {
	filename := firstNonEmpty(payload.Data.FileName, req.Name)
	size := payload.Data.Size
	if size <= 0 {
		size = req.Size
	}
	result := VideoPreviewResult{
		Filename: filename,
		Size:     size,
	}
	for _, item := range payload.Data.VideoList {
		if !item.Accessable || strings.TrimSpace(item.VideoInfo.URL) == "" {
			continue
		}
		qualityReferer := quarkLikeVideoPreviewReferer(providerKey, referer, item.VideoInfo.URL)
		headers := map[string]string{
			"User-Agent": userAgent,
			"Cookie":     cookie,
		}
		if qualityReferer != "" {
			headers["Referer"] = qualityReferer
		}
		format, contentType := quarkVideoPreviewFormat(item.VideoInfo)
		key := providerKey + "-" + strings.ToLower(strings.TrimSpace(item.Resolution))
		if key == providerKey+"-" {
			key = providerKey + "-video"
		}
		quality := VideoPreviewQuality{
			Key:         key,
			Label:       quarkVideoQualityLabel(item),
			Resolution:  item.Resolution,
			URL:         item.VideoInfo.URL,
			Filename:    filename,
			Size:        firstPositiveInt64(item.VideoInfo.Size, size),
			Format:      format,
			ContentType: contentType,
			Width:       item.VideoInfo.Width,
			Height:      item.VideoInfo.Height,
			Bitrate:     item.VideoInfo.Bitrate,
			Accessable:  item.Accessable,
			Right:       firstNonEmpty(item.Right, item.MemberRight),
			UserAgent:   userAgent,
			Cookie:      cookie,
			Referer:     qualityReferer,
			Headers:     cloneStringMap(headers),
		}
		result.Qualities = append(result.Qualities, quality)
	}
	result.DefaultQuality = quarkDefaultVideoQuality(result.Qualities, payload.Data.DefaultResolution)
	return result
}

func quarkLikeVideoPreviewReferer(providerKey, defaultReferer, mediaURL string) string {
	if providerKey == "uc" {
		return ucVideoPreviewReferer(mediaURL)
	}
	return defaultReferer
}

func quarkVideoPreviewFormat(info quarkVideoPlayInfo) (string, string) {
	text := strings.ToLower(info.URL + ";" + info.Format + ";" + info.FileFormat)
	if strings.Contains(text, "m3u8") {
		return "hls", "application/vnd.apple.mpegurl"
	}
	return "video", "video/mp4"
}

func quarkVideoQualityLabel(item quarkVideoPlayItem) string {
	resolution := strings.ToLower(strings.TrimSpace(item.Resolution))
	height := item.VideoInfo.Height
	switch resolution {
	case "4k":
		return "4K"
	case "2k":
		return "2K"
	case "super":
		return "超清"
	case "high":
		return "高清"
	case "normal":
		return "标清"
	case "low":
		return "流畅"
	default:
		if height > 0 {
			return fmt.Sprintf("%dP", height)
		}
		return firstNonEmpty(item.Resolution, "播放")
	}
}

func quarkDefaultVideoQuality(qualities []VideoPreviewQuality, defaultResolution string) string {
	defaultResolution = strings.ToLower(strings.TrimSpace(defaultResolution))
	for _, quality := range qualities {
		if strings.ToLower(strings.TrimSpace(quality.Resolution)) == defaultResolution {
			return quality.Key
		}
	}
	if len(qualities) > 0 {
		return qualities[0].Key
	}
	return ""
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func (c *QuarkClient) acquireDownloadToken(ctx context.Context, cookie string) (string, error) {
	endpoint := "https://drive-social-api.quark.cn/1/clouddrive/chat/conv/file/acquire_dl_token?pr=ucpro&fr=pc"
	body, err := json.Marshal(map[string]any{
		"conversation_id":   "",
		"conversation_type": 2,
		"msg_id":            "",
	})
	if err != nil {
		return "", err
	}
	done := applog.Begin("netdisk.quark", "acquire_download_token", map[string]any{"url": endpoint})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		done(err, nil)
		return "", err
	}
	httpReq.Header.Set("User-Agent", quarkPCUserAgent)
	httpReq.Header.Set("Accept", "application/json, text/plain, */*")
	httpReq.Header.Set("Content-Type", "application/json;charset=utf-8")
	httpReq.Header.Set("Referer", "https://pan.quark.cn/")
	httpReq.Header.Set("Origin", "https://pan.quark.cn")
	httpReq.Header.Set("Cookie", quarkCookieWithCapturedResponseFields(ctx, cookie))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		done(err, nil)
		return "", err
	}
	defer resp.Body.Close()
	captureQuarkResponseCookies(ctx, resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{"status": resp.StatusCode})
		return "", ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("quark download token failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
		done(err, map[string]any{"status": resp.StatusCode})
		return "", err
	}
	var payload struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token            string `json:"token"`
			ExpiredTimestamp int64  `json:"expired_timestamp"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		done(err, map[string]any{"status": resp.StatusCode})
		return "", err
	}
	if payload.Status != 0 && payload.Status != 200 {
		err := fmt.Errorf("quark download token failed: %s", firstNonEmpty(payload.Message, strconv.Itoa(payload.Status), strconv.Itoa(payload.Code)))
		done(err, map[string]any{"status": resp.StatusCode, "upstream_status": payload.Status, "upstream_code": payload.Code})
		return "", err
	}
	token := strings.TrimSpace(payload.Data.Token)
	if token == "" {
		err := errors.New("quark download token is missing")
		done(err, map[string]any{"status": resp.StatusCode, "upstream_status": payload.Status, "upstream_code": payload.Code})
		return "", err
	}
	done(nil, map[string]any{
		"status":            resp.StatusCode,
		"upstream_code":     payload.Code,
		"expired_timestamp": payload.Data.ExpiredTimestamp,
		"has_token":         true,
	})
	return token, nil
}

func collectQuarkDownloadCookie(resp *http.Response, fallbackCookie string) string {
	values := make([]string, 0, 1)
	if resp != nil {
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "__puus" && strings.TrimSpace(cookie.Value) != "" {
				values = append(values, cookie.Name+"="+cookie.Value)
			}
		}
	}
	if len(values) == 0 {
		if value := strings.TrimSpace(parseCookieFields(fallbackCookie)["__puus"]); value != "" {
			values = append(values, "__puus="+value)
		}
	}
	return strings.Join(values, "; ")
}

func collectUCDownloadCookie(resp *http.Response, fallbackCookie string) string {
	values := make([]string, 0, 1)
	if resp != nil {
		for _, cookie := range resp.Cookies() {
			if (cookie.Name == "__puus" || cookie.Name == "__pus") && strings.TrimSpace(cookie.Value) != "" {
				values = append(values, cookie.Name+"="+cookie.Value)
			}
		}
	}
	if len(values) == 0 {
		fields := parseCookieFields(fallbackCookie)
		if value := strings.TrimSpace(fields["__puus"]); value != "" {
			values = append(values, "__puus="+value)
		} else if value := strings.TrimSpace(fields["__pus"]); value != "" {
			values = append(values, "__pus="+value)
		}
	}
	return strings.Join(values, "; ")
}

func ucDownloadReferer(downloadURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(downloadURL))
	if err != nil {
		return "https://drive.uc.cn/"
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "cdn.yun.cn" || strings.HasSuffix(host, ".cdn.yun.cn") {
		return ""
	}
	return "https://drive.uc.cn/"
}

func ucVideoPreviewReferer(mediaURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(mediaURL))
	if err != nil {
		return "https://drive.uc.cn/"
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.HasPrefix(host, "video-play") && (host == "cdn.yun.cn" || strings.HasSuffix(host, ".cdn.yun.cn")) {
		return "https://drive.uc.cn/"
	}
	return ucDownloadReferer(mediaURL)
}

func providerBodySnippet(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return strings.TrimSpace(string(data))
}

func providerCookieDebugFields(cookie string) map[string]any {
	fields := parseCookieFields(cookie)
	return map[string]any{
		"present":   strings.TrimSpace(cookie) != "",
		"length":    len(cookie),
		"has_pus":   strings.TrimSpace(fields["__pus"]) != "",
		"has_puus":  strings.TrimSpace(fields["__puus"]) != "",
		"has_pugs":  strings.TrimSpace(fields["__pugs"]) != "",
		"has_kps":   strings.TrimSpace(fields["kps"]) != "",
		"has_pcsrf": strings.TrimSpace(fields["__pcsrftoken"]) != "",
	}
}

func responseCookieNames(resp *http.Response) []string {
	if resp == nil {
		return nil
	}
	names := make([]string, 0, len(resp.Cookies()))
	seen := make(map[string]struct{})
	for _, cookie := range resp.Cookies() {
		name := strings.TrimSpace(cookie.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func (c *QuarkClient) CreateFolder(ctx context.Context, account Account, cookie string, req CreateFolderRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return errors.New("folder name is required")
	}
	parentID := strings.TrimSpace(req.ParentID)
	if parentID == "" {
		parentID = "0"
	}
	body := map[string]any{
		"pdir_fid":      parentID,
		"file_name":     name,
		"dir_init_lock": false,
		"dir_path":      "",
	}
	var out quarkStatusResponse
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/file?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return err
	}
	return out.err("quark create folder")
}

func (c *QuarkClient) RenameFile(ctx context.Context, account Account, cookie string, req RenameFileRequest) error {
	if strings.TrimSpace(req.FileID) == "" {
		return errors.New("file id is required")
	}
	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		return errors.New("new name is required")
	}
	body := map[string]any{
		"fid":       req.FileID,
		"file_name": newName,
	}
	var out quarkStatusResponse
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/file/rename?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return err
	}
	return out.err("quark rename")
}

func (c *QuarkClient) DeleteFiles(ctx context.Context, account Account, cookie string, req DeleteFilesRequest) error {
	if len(req.FileIDs) == 0 {
		return errors.New("file ids are required")
	}
	body := map[string]any{
		"action_type":  2,
		"filelist":     req.FileIDs,
		"exclude_fids": []string{},
	}
	var out quarkStatusResponse
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/file/delete?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return err
	}
	return out.err("quark delete")
}

func (c *QuarkClient) CopyFiles(ctx context.Context, account Account, cookie string, req FileTransferRequest) error {
	return c.transferFiles(ctx, cookie, "copy", req)
}

func (c *QuarkClient) MoveFiles(ctx context.Context, account Account, cookie string, req FileTransferRequest) error {
	return c.transferFiles(ctx, cookie, "move", req)
}

func (c *QuarkClient) CreateShare(ctx context.Context, account Account, cookie string, req CreateShareRequest) (ShareResult, error) {
	fileIDs := cleanIDs(req.FileIDs)
	if len(fileIDs) == 0 {
		return ShareResult{}, errors.New("file ids are required")
	}
	expireDays := req.ExpireDays
	if expireDays < 0 {
		expireDays = 0
	}
	password := strings.TrimSpace(req.Password)
	body := map[string]any{
		"fid_list":     fileIDs,
		"title":        strings.TrimSpace(req.Title),
		"url_type":     1,
		"expired_type": 1,
	}
	if password != "" {
		body["url_type"] = 2
		body["passcode"] = password
	}
	if expireDays > 0 {
		body["expired_type"] = 2
		body["expired_at"] = time.Now().Add(time.Duration(expireDays) * 24 * time.Hour).UnixMilli()
	}
	var out quarkTransferResponse
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/share?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return ShareResult{}, err
	}
	if out.Status != 0 && out.Status != 200 {
		return ShareResult{}, fmt.Errorf("quark share failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	shareID := out.Data.ShareID
	if shareID == "" && out.Data.TaskID != "" {
		var err error
		shareID, err = c.waitQuarkShareTask(ctx, cookie, out.Data.TaskID, 1000)
		if err != nil {
			return ShareResult{}, err
		}
	}
	if shareID == "" {
		return ShareResult{}, errors.New("quark did not return a share id")
	}
	detail, err := c.quarkShareDetail(ctx, cookie, shareID)
	if err != nil {
		return ShareResult{}, err
	}
	detail.Password = firstNonEmpty(detail.Password, password)
	detail.ExpireDays = expireDays
	if detail.Title == "" {
		detail.Title = req.Title
	}
	return detail, nil
}

func (c *QuarkClient) ListAccountShares(ctx context.Context, account Account, cookie string, req ListAccountSharesRequest) (AccountShareList, error) {
	page, pageSize := normalizeListPage(req.Page, req.PageSize, 50)
	values := url.Values{}
	values.Set("pr", "ucpro")
	values.Set("fr", "pc")
	values.Set("uc_param_str", "")
	values.Set("_page", strconv.Itoa(page))
	values.Set("_size", strconv.Itoa(pageSize))
	values.Set("_order_field", "created_at")
	values.Set("_order_type", "desc")
	values.Set("_fetch_total", "1")
	values.Set("_fetch_notify_follow", "1")
	endpoint := "https://drive-pc.quark.cn/1/clouddrive/share/mypage/detail?" + values.Encode()
	var payload struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			List  []json.RawMessage `json:"list"`
			Total int               `json:"total"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return AccountShareList{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return AccountShareList{}, fmt.Errorf("quark share list failed: %s", firstNonEmpty(payload.Message, strconv.Itoa(payload.Status), strconv.Itoa(payload.Code)))
	}
	shares := make([]AccountShareItem, 0, len(payload.Data.List))
	for _, rawItem := range payload.Data.List {
		rawMap := rawMapFromJSON(rawItem)
		shareID := firstNonEmpty(stringField(rawMap, "share_id"), stringField(rawMap, "shareId"), stringField(rawMap, "id"))
		title := firstNonEmpty(
			stringField(rawMap, "title"),
			stringField(rawMap, "share_title"),
			stringField(rawMap, "file_name"),
			stringField(rawMap, "first_file_name"),
			stringField(rawMap, "name"),
			"分享_"+shareID,
		)
		fileCount := firstPositiveInt(
			int(numericValue(rawMap["file_count"])),
			int(numericValue(rawMap["file_num"])),
			int(numericValue(rawMap["fileNum"])),
			int(numericValue(rawMap["num"])),
			rawListLength(rawMap["fid_list"]),
			rawListLength(rawMap["filelist"]),
		)
		viewCount := firstPositiveInt(
			int(numericValue(rawMap["view_count"])),
			int(numericValue(rawMap["viewCount"])),
			int(numericValue(rawMap["visit_count"])),
			int(numericValue(rawMap["read_count"])),
		)
		shares = append(shares, AccountShareItem{
			ID:        firstNonEmpty(shareID, title),
			Provider:  ProviderQuark,
			AccountID: account.ID,
			ShareID:   shareID,
			Title:     title,
			URL: normalizeQuarkPreviewURL(firstNonEmpty(
				stringField(rawMap, "share_url"),
				stringField(rawMap, "shareUrl"),
				stringField(rawMap, "url"),
				stringField(rawMap, "short_url"),
			)),
			Password:  firstNonEmpty(stringField(rawMap, "passcode"), stringField(rawMap, "password"), stringField(rawMap, "pwd")),
			FileCount: fileCount,
			ViewCount: viewCount,
			CreatedAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["created_at"]),
				numericValue(rawMap["create_time"]),
				numericValue(rawMap["ctime"]),
			)),
			ExpiredAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["expired_at"]),
				numericValue(rawMap["expire_time"]),
				numericValue(rawMap["expired_time"]),
			)),
			Raw: rawMap,
		})
	}
	return AccountShareList{
		Account: account,
		Shares:  shares,
		HasMore: hasMoreListPage(payload.Data.Total, page, pageSize, len(shares)),
		Next:    PageHint{Page: page + 1, PageSize: pageSize},
	}, nil
}

func (c *QuarkClient) CancelShares(ctx context.Context, account Account, cookie string, req CancelSharesRequest) error {
	shareIDs := cleanIDs(req.ShareIDs)
	if len(shareIDs) == 0 {
		return errors.New("share ids are required")
	}
	body := map[string]any{
		"share_ids": shareIDs,
	}
	var out quarkTransferResponse
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/share/delete?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return err
	}
	if out.Status != 0 && out.Status != 200 {
		return fmt.Errorf("quark cancel share failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	if out.Data.TaskID != "" && !out.Data.Finish {
		return c.waitQuarkTask(ctx, cookie, out.Data.TaskID, out.Metadata.TQGap)
	}
	return nil
}

func (c *QuarkClient) ListRecycle(ctx context.Context, account Account, cookie string, req ListRecycleRequest) (RecycleList, error) {
	page, pageSize := normalizeListPage(req.Page, req.PageSize, 100)
	values := url.Values{}
	values.Set("pr", "ucpro")
	values.Set("fr", "pc")
	values.Set("uc_param_str", "")
	values.Set("_page", strconv.Itoa(page))
	values.Set("_size", strconv.Itoa(pageSize))
	values.Set("_fetch_total", "1")
	values.Set("_sort", "updated_at:desc")
	endpoint := "https://drive-pc.quark.cn/1/clouddrive/file/recycle/list?" + values.Encode()
	var payload struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			List  []json.RawMessage `json:"list"`
			Total int               `json:"total"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, endpoint, cookie, &payload); err != nil {
		return RecycleList{}, err
	}
	if payload.Status != 0 && payload.Status != 200 {
		return RecycleList{}, fmt.Errorf("quark recycle list failed: %s", firstNonEmpty(payload.Message, strconv.Itoa(payload.Status), strconv.Itoa(payload.Code)))
	}
	files := make([]RecycleItem, 0, len(payload.Data.List))
	for _, rawItem := range payload.Data.List {
		item, rawMap := decodeQuarkFileItem(rawItem)
		files = append(files, RecycleItem{
			ID:          firstNonEmpty(item.FID, stringField(rawMap, "id"), item.FileName),
			OperationID: firstNonEmpty(stringField(rawMap, "record_id"), stringField(rawMap, "recordId")),
			Provider:    ProviderQuark,
			AccountID:   account.ID,
			Name:        item.FileName,
			Path:        stringField(rawMap, "path"),
			ParentID:    item.PDirFID,
			IsDir:       item.Dir,
			Size:        item.Size,
			MD5:         item.MD5,
			DeletedAt: unixTime(firstNonZeroInt64(
				numericValue(rawMap["deleted_at"]),
				numericValue(rawMap["delete_time"]),
				numericValue(rawMap["trashed_at"]),
				numericValue(rawMap["updated_at"]),
			)),
			UpdatedAt: unixTime(item.UpdatedAt),
			Raw:       rawMap,
		})
	}
	return RecycleList{
		Account: account,
		Files:   files,
		HasMore: hasMoreListPage(payload.Data.Total, page, pageSize, len(files)),
		Next:    PageHint{Page: page + 1, PageSize: pageSize},
	}, nil
}

func (c *QuarkClient) RestoreRecycle(ctx context.Context, account Account, cookie string, req RecycleMutationRequest) error {
	return c.mutateRecycle(ctx, cookie, "recover", req)
}

func (c *QuarkClient) DeleteRecycle(ctx context.Context, account Account, cookie string, req RecycleMutationRequest) error {
	return c.mutateRecycle(ctx, cookie, "remove", req)
}

func (c *QuarkClient) mutateRecycle(ctx context.Context, cookie, operation string, req RecycleMutationRequest) error {
	ids := cleanIDs(req.ItemIDs)
	if len(ids) == 0 {
		return errors.New("recycle item ids are required")
	}
	values := url.Values{}
	values.Set("pr", "ucpro")
	values.Set("fr", "pc")
	values.Set("uc_param_str", "")
	endpoint := "https://drive-pc.quark.cn/1/clouddrive/file/recycle/" + operation + "?" + values.Encode()
	var out quarkTransferResponse
	if err := c.postJSON(ctx, endpoint, cookie, map[string]any{"select_mode": 2, "record_list": ids}, &out); err != nil {
		return err
	}
	if out.Status != 0 && out.Status != 200 {
		return fmt.Errorf("quark recycle %s failed: %s", operation, firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	if out.Data.TaskID != "" && !out.Data.Finish {
		return c.waitQuarkTask(ctx, cookie, out.Data.TaskID, out.Metadata.TQGap)
	}
	return nil
}

func (c *QuarkClient) transferFiles(ctx context.Context, cookie, opera string, req FileTransferRequest) error {
	fileIDs, err := quarkTransferFileIDs(req)
	if err != nil {
		return err
	}
	targetParentID := strings.TrimSpace(req.TargetParentID)
	if targetParentID == "" {
		targetParentID = "0"
	}
	body := map[string]any{
		"action_type":  1,
		"to_pdir_fid":  targetParentID,
		"filelist":     fileIDs,
		"exclude_fids": []string{},
	}
	var out quarkTransferResponse
	endpoint := "https://drive-pc.quark.cn/1/clouddrive/file/" + opera + "?pr=ucpro&fr=pc&uc_param_str="
	if err := c.postJSON(ctx, endpoint, cookie, body, &out); err != nil {
		return err
	}
	if out.Status != 0 && out.Status != 200 {
		return fmt.Errorf("quark %s failed: %s", opera, firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	if out.Data.TaskID != "" && !out.Data.Finish {
		return c.waitQuarkTask(ctx, cookie, out.Data.TaskID, out.Metadata.TQGap)
	}
	return nil
}

func (c *QuarkClient) getJSON(ctx context.Context, endpoint, cookie string, out any) error {
	done := applog.Begin("netdisk.quark", "http_get", map[string]any{"url": endpoint})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		done(err, nil)
		return err
	}
	req.Header.Set("User-Agent", quarkPCUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://pan.quark.cn/")
	req.Header.Set("Origin", "https://pan.quark.cn")
	req.Header.Set("Cookie", quarkCookieWithCapturedResponseFields(ctx, cookie))
	resp, err := c.http.Do(req)
	if err != nil {
		done(err, nil)
		return err
	}
	defer resp.Body.Close()
	captureQuarkResponseCookies(ctx, resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{"status": resp.StatusCode})
		return ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		err := fmt.Errorf("quark request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
		done(err, map[string]any{"status": resp.StatusCode})
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(out)
	done(err, map[string]any{"status": resp.StatusCode})
	return err
}

func (c *UCClient) getJSON(ctx context.Context, endpoint, cookie string, out any) error {
	done := applog.Begin("netdisk.uc", "http_get", map[string]any{"url": endpoint})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		done(err, nil)
		return err
	}
	req.Header.Set("User-Agent", ucPCUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://drive.uc.cn/")
	req.Header.Set("Origin", "https://drive.uc.cn")
	req.Header.Set("Cookie", quarkCookieWithCapturedResponseFields(ctx, cookie))
	resp, err := c.http.Do(req)
	if err != nil {
		done(err, nil)
		return err
	}
	defer resp.Body.Close()
	captureQuarkResponseCookies(ctx, resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{"status": resp.StatusCode})
		return ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if err := ucFriendlyHTTPError(resp.StatusCode, detail); err != nil {
			done(err, map[string]any{"status": resp.StatusCode})
			return err
		}
		err := ucOperationError("request", strings.TrimSpace(string(detail)), resp.StatusCode, resp.StatusCode)
		done(err, map[string]any{"status": resp.StatusCode})
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(out)
	done(err, map[string]any{"status": resp.StatusCode})
	return err
}

func (c *UCClient) postJSON(ctx context.Context, endpoint, cookie string, body any, out any) error {
	done := applog.Begin("netdisk.uc", "http_post_json", map[string]any{"url": endpoint})
	data, err := json.Marshal(body)
	if err != nil {
		done(err, nil)
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		done(err, nil)
		return err
	}
	req.Header.Set("User-Agent", ucPCUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	req.Header.Set("Referer", "https://drive.uc.cn/")
	req.Header.Set("Origin", "https://drive.uc.cn")
	req.Header.Set("Cookie", quarkCookieWithCapturedResponseFields(ctx, cookie))
	resp, err := c.http.Do(req)
	if err != nil {
		done(err, nil)
		return err
	}
	defer resp.Body.Close()
	captureQuarkResponseCookies(ctx, resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{"status": resp.StatusCode})
		return ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if err := ucFriendlyHTTPError(resp.StatusCode, detail); err != nil {
			done(err, map[string]any{"status": resp.StatusCode})
			return err
		}
		err := ucOperationError("request", strings.TrimSpace(string(detail)), resp.StatusCode, resp.StatusCode)
		done(err, map[string]any{"status": resp.StatusCode})
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(out)
	done(err, map[string]any{"status": resp.StatusCode})
	return err
}

type quarkStatusResponse struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type quarkTransferResponse struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		TaskID  string `json:"task_id"`
		ShareID string `json:"share_id"`
		Finish  bool   `json:"finish"`
	} `json:"data"`
	Metadata struct {
		TQGap int `json:"tq_gap"`
	} `json:"metadata"`
}

func (r quarkStatusResponse) err(action string) error {
	if r.Status == 0 || r.Status == 200 {
		return nil
	}
	return fmt.Errorf("%s failed: %s", action, firstNonEmpty(r.Message, strconv.Itoa(r.Status), strconv.Itoa(r.Code)))
}

func ucStatusError(r quarkStatusResponse, operation string) error {
	if r.Status == 0 || r.Status == 200 {
		return nil
	}
	return ucOperationError(operation, r.Message, r.Status, r.Code)
}

func ucOperationError(operation string, upstream string, status int, code any) error {
	label := ucOperationLabel(operation)
	message := ucFriendlyUpstreamMessage(upstream, status, code)
	if message == "" {
		message = "上游服务异常，请稍后重试"
	}
	return fmt.Errorf("%s：%s", label, message)
}

func ucOperationLabel(operation string) string {
	switch strings.TrimSpace(operation) {
	case "quota":
		return "获取 UC 网盘容量失败"
	case "list":
		return "加载 UC 网盘文件列表失败"
	case "search":
		return "搜索 UC 网盘文件失败"
	case "download":
		return "获取 UC 网盘下载链接失败"
	case "video_preview":
		return "获取 UC 网盘视频播放地址失败"
	case "upload_pre", "upload":
		return "上传 UC 网盘文件失败"
	case "upload_hash":
		return "校验 UC 网盘文件失败"
	case "oss_upload_part":
		return "上传 UC 网盘文件分片失败"
	case "upload_auth":
		return "获取 UC 网盘上传授权失败"
	case "oss_complete":
		return "合并 UC 网盘上传分片失败"
	case "upload_finish":
		return "完成 UC 网盘上传失败"
	case "create_folder":
		return "新建 UC 网盘文件夹失败"
	case "rename":
		return "重命名 UC 网盘文件失败"
	case "delete":
		return "删除 UC 网盘文件失败"
	case "copy":
		return "复制 UC 网盘文件失败"
	case "move":
		return "移动 UC 网盘文件失败"
	case "share":
		return "创建 UC 网盘分享失败"
	case "share_detail":
		return "获取 UC 网盘分享信息失败"
	case "share_list":
		return "加载 UC 网盘分享列表失败"
	case "cancel_share":
		return "取消 UC 网盘分享失败"
	case "recycle":
		return "加载 UC 网盘回收站失败"
	case "task":
		return "UC 网盘任务处理失败"
	case "share_task":
		return "创建 UC 网盘分享失败"
	default:
		return "UC 网盘请求失败"
	}
}

func ucFriendlyUpstreamMessage(upstream string, status int, code any) string {
	message := strings.TrimSpace(upstream)
	if hasCJK(message) {
		return message
	}
	normalized := strings.ToLower(message)
	codeText := stringifyID(code)
	if codeText == "0" {
		codeText = ""
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || codeText == "401" || codeText == "40100" || codeText == "40101":
		return "账号登录状态已失效，请重新登录 UC 网盘"
	case status == http.StatusNotFound || strings.Contains(normalized, "not found"):
		return "文件或分享不存在，可能已被删除或失效"
	case status == http.StatusTooManyRequests || strings.Contains(normalized, "too many") || strings.Contains(normalized, "rate"):
		return "请求过于频繁，请稍后重试"
	case strings.Contains(normalized, "pwd") || strings.Contains(normalized, "passcode") || strings.Contains(normalized, "password"):
		return "提取码错误或已失效"
	case strings.Contains(normalized, "permission") || strings.Contains(normalized, "forbidden") || strings.Contains(normalized, "unauthorized"):
		return "没有权限访问该文件，请确认账号或分享状态"
	case strings.Contains(normalized, "expired") || strings.Contains(normalized, "expire"):
		return "分享或登录凭证已过期，请刷新后重试"
	case strings.Contains(normalized, "too big") || strings.Contains(normalized, "limit"):
		return "文件超过当前账号限制"
	case strings.Contains(normalized, "cors"):
		return "接口跨域校验失败，当前 UC 网盘线路不可用"
	case strings.Contains(normalized, "timeout"):
		return "请求超时，请检查网络后重试"
	}
	if codeText != "" {
		return "上游返回错误码 " + codeText
	}
	if status > 0 {
		return "上游 HTTP 状态 " + strconv.Itoa(status)
	}
	return ""
}

func hasCJK(value string) bool {
	for _, r := range value {
		if r >= '\u4e00' && r <= '\u9fff' {
			return true
		}
	}
	return false
}

func ucFriendlyHTTPError(statusCode int, body []byte) error {
	if statusCode <= 0 || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var uploadPre quarkUploadPreResponse
	if err := json.Unmarshal(body, &uploadPre); err == nil {
		if err := uploadPre.LimitError("netdisk.uc"); err != nil {
			return err
		}
	}
	return nil
}

func (c *QuarkClient) postJSON(ctx context.Context, endpoint, cookie string, body any, out any) error {
	done := applog.Begin("netdisk.quark", "http_post_json", map[string]any{"url": endpoint})
	data, err := json.Marshal(body)
	if err != nil {
		done(err, nil)
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		done(err, nil)
		return err
	}
	req.Header.Set("User-Agent", quarkPCUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	req.Header.Set("Referer", "https://pan.quark.cn/")
	req.Header.Set("Origin", "https://pan.quark.cn")
	req.Header.Set("Cookie", quarkCookieWithCapturedResponseFields(ctx, cookie))
	resp, err := c.http.Do(req)
	if err != nil {
		done(err, nil)
		return err
	}
	defer resp.Body.Close()
	captureQuarkResponseCookies(ctx, resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		done(ErrProviderUnauthorized, map[string]any{"status": resp.StatusCode})
		return ErrProviderUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		err := fmt.Errorf("quark request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
		done(err, map[string]any{"status": resp.StatusCode})
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(out)
	done(err, map[string]any{"status": resp.StatusCode})
	return err
}

func quarkTransferFileIDs(req FileTransferRequest) ([]string, error) {
	ids := make([]string, 0, len(req.Files))
	for _, file := range req.Files {
		fileID := strings.TrimSpace(file.FileID)
		if fileID != "" {
			ids = append(ids, fileID)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("file ids are required")
	}
	return ids, nil
}

func cleanIDs(ids []string) []string {
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			cleaned = append(cleaned, id)
		}
	}
	return cleaned
}

func mustJSONList(values []string) string {
	data, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func baiduFIDList(values []string) (string, error) {
	ids := cleanIDs(values)
	if len(ids) == 0 {
		return "", errors.New("recycle item ids are required")
	}
	numericIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		numericID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || numericID <= 0 {
			return "", fmt.Errorf("invalid Baidu recycle item id %q", id)
		}
		numericIDs = append(numericIDs, numericID)
	}
	data, err := json.Marshal(numericIDs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *QuarkClient) quarkShareDetail(ctx context.Context, cookie, shareID string) (ShareResult, error) {
	body := map[string]any{"share_id": shareID}
	var out struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			ShareID  string `json:"share_id"`
			ShareURL string `json:"share_url"`
			URL      string `json:"url"`
			Passcode string `json:"passcode"`
			Password string `json:"password"`
			Title    string `json:"title"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, "https://drive-pc.quark.cn/1/clouddrive/share/password?pr=ucpro&fr=pc&uc_param_str=", cookie, body, &out); err != nil {
		return ShareResult{}, err
	}
	if out.Status != 0 && out.Status != 200 {
		return ShareResult{}, fmt.Errorf("quark share detail failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
	}
	shareURL := firstNonEmpty(out.Data.ShareURL, out.Data.URL)
	if shareURL == "" {
		return ShareResult{}, errors.New("quark did not return a share url")
	}
	return ShareResult{
		ShareID:  firstNonEmpty(out.Data.ShareID, shareID),
		URL:      shareURL,
		Password: firstNonEmpty(out.Data.Passcode, out.Data.Password),
		Title:    out.Data.Title,
	}, nil
}

func (c *UCClient) ucShareDetail(ctx context.Context, cookie, shareID string) (ShareResult, error) {
	body := map[string]any{"share_id": shareID}
	var out struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			ShareID  string `json:"share_id"`
			ShareURL string `json:"share_url"`
			URL      string `json:"url"`
			Passcode string `json:"passcode"`
			Password string `json:"password"`
			Title    string `json:"title"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, ucQuarkEndpoint("/1/clouddrive/share/password", nil), cookie, body, &out); err != nil {
		return ShareResult{}, err
	}
	if out.Status != 0 && out.Status != 200 {
		return ShareResult{}, ucOperationError("share_detail", out.Message, out.Status, out.Code)
	}
	shareURL := firstNonEmpty(out.Data.ShareURL, out.Data.URL)
	if shareURL == "" {
		return ShareResult{}, errors.New("获取 UC 网盘分享信息失败：上游未返回分享链接")
	}
	return ShareResult{
		ShareID:  firstNonEmpty(out.Data.ShareID, shareID),
		URL:      normalizeUCPreviewURL(shareURL),
		Password: firstNonEmpty(out.Data.Passcode, out.Data.Password),
		Title:    out.Data.Title,
	}, nil
}

func (c *QuarkClient) waitQuarkTask(ctx context.Context, cookie, taskID string, pollGapMS int) error {
	if pollGapMS <= 0 {
		pollGapMS = 500
	}
	for retry := 0; retry < 30; retry++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(pollGapMS) * time.Millisecond):
		}
		values := url.Values{}
		values.Set("pr", "ucpro")
		values.Set("fr", "pc")
		values.Set("uc_param_str", "")
		values.Set("task_id", taskID)
		values.Set("retry_index", strconv.Itoa(retry))
		endpoint := "https://drive-pc.quark.cn/1/clouddrive/task?" + values.Encode()
		var out struct {
			Status  int    `json:"status"`
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"data"`
		}
		if err := c.getJSON(ctx, endpoint, cookie, &out); err != nil {
			return err
		}
		if out.Status != 0 && out.Status != 200 {
			return fmt.Errorf("quark task failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
		}
		switch out.Data.Status {
		case 2:
			return nil
		case 3:
			return fmt.Errorf("quark task failed: %s", firstNonEmpty(out.Data.Message, "task failed"))
		}
	}
	return errors.New("quark task timeout")
}

func (c *UCClient) waitUCTask(ctx context.Context, cookie, taskID string, pollGapMS int) error {
	if pollGapMS <= 0 {
		pollGapMS = 500
	}
	for retry := 0; retry < 30; retry++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(pollGapMS) * time.Millisecond):
		}
		values := ucQuarkValues()
		values.Set("task_id", taskID)
		values.Set("retry_index", strconv.Itoa(retry))
		endpoint := "https://drive-pc.quark.cn/1/clouddrive/task?" + values.Encode()
		var out struct {
			Status  int    `json:"status"`
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"data"`
		}
		if err := c.getJSON(ctx, endpoint, cookie, &out); err != nil {
			return err
		}
		if out.Status != 0 && out.Status != 200 {
			return ucOperationError("task", out.Message, out.Status, out.Code)
		}
		switch out.Data.Status {
		case 2:
			return nil
		case 3:
			return ucOperationError("task", out.Data.Message, out.Data.Status, out.Data.Status)
		}
	}
	return errors.New("UC 网盘任务处理超时，请稍后重试")
}

func (c *QuarkClient) waitQuarkShareTask(ctx context.Context, cookie, taskID string, pollGapMS int) (string, error) {
	if pollGapMS <= 0 {
		pollGapMS = 1000
	}
	for retry := 0; retry < 12; retry++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(pollGapMS) * time.Millisecond):
		}
		values := url.Values{}
		values.Set("pr", "ucpro")
		values.Set("fr", "pc")
		values.Set("uc_param_str", "")
		values.Set("task_id", taskID)
		values.Set("retry_index", strconv.Itoa(retry))
		endpoint := "https://drive-pc.quark.cn/1/clouddrive/task?" + values.Encode()
		var out struct {
			Status  int    `json:"status"`
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
				ShareID string `json:"share_id"`
			} `json:"data"`
		}
		if err := c.getJSON(ctx, endpoint, cookie, &out); err != nil {
			return "", err
		}
		if out.Status != 0 && out.Status != 200 {
			return "", fmt.Errorf("quark share task failed: %s", firstNonEmpty(out.Message, strconv.Itoa(out.Status), strconv.Itoa(out.Code)))
		}
		switch out.Data.Status {
		case 2:
			if out.Data.ShareID == "" {
				return "", errors.New("quark share task completed without share id")
			}
			return out.Data.ShareID, nil
		case 3:
			return "", fmt.Errorf("quark share task failed: %s", firstNonEmpty(out.Data.Message, "task failed"))
		}
	}
	return "", errors.New("quark share task timeout")
}

func (c *UCClient) waitUCShareTask(ctx context.Context, cookie, taskID string, pollGapMS int) (string, error) {
	if pollGapMS <= 0 {
		pollGapMS = 1000
	}
	for retry := 0; retry < 12; retry++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(pollGapMS) * time.Millisecond):
		}
		values := ucQuarkValues()
		values.Set("task_id", taskID)
		values.Set("retry_index", strconv.Itoa(retry))
		endpoint := "https://drive-pc.quark.cn/1/clouddrive/task?" + values.Encode()
		var out struct {
			Status  int    `json:"status"`
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
				ShareID string `json:"share_id"`
			} `json:"data"`
		}
		if err := c.getJSON(ctx, endpoint, cookie, &out); err != nil {
			return "", err
		}
		if out.Status != 0 && out.Status != 200 {
			return "", ucOperationError("share_task", out.Message, out.Status, out.Code)
		}
		switch out.Data.Status {
		case 2:
			if out.Data.ShareID == "" {
				return "", errors.New("创建 UC 网盘分享失败：任务完成但未返回分享 ID")
			}
			return out.Data.ShareID, nil
		case 3:
			return "", ucOperationError("share_task", out.Data.Message, out.Data.Status, out.Data.Status)
		}
	}
	return "", errors.New("创建 UC 网盘分享失败：任务处理超时，请稍后重试")
}

func stringifyID(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	default:
		return fmt.Sprint(value)
	}
}

func thumbnailFromMap(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func thumbnailFromRawValue(value any) string {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		lower := strings.ToLower(text)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/") {
			return text
		}
	case map[string]any:
		for _, key := range []string{"thumbnail_url", "thumbnail", "t_url", "big_thumbnail", "s_url", "cover_url", "cover", "image_url", "url", "src"} {
			if value, ok := typed[key]; ok {
				if thumbnail := thumbnailFromRawValue(value); thumbnail != "" {
					return thumbnail
				}
			}
		}
		for key, value := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "thumb") || strings.Contains(lower, "cover") || strings.Contains(lower, "image") || strings.Contains(lower, "pic") || lower == "t_url" || lower == "s_url" {
				if thumbnail := thumbnailFromRawValue(value); thumbnail != "" {
					return thumbnail
				}
			}
		}
		for _, value := range typed {
			switch value.(type) {
			case map[string]any, []any:
				if thumbnail := thumbnailFromRawValue(value); thumbnail != "" {
					return thumbnail
				}
			}
		}
	case []any:
		for _, value := range typed {
			if thumbnail := thumbnailFromRawValue(value); thumbnail != "" {
				return thumbnail
			}
		}
	}
	return ""
}

func normalizeQuarkPreviewURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "&amp;", "&")
	value = normalizeBrokenURLScheme(value)
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") {
		return value
	}
	if strings.HasPrefix(value, "/") {
		return "https://drive-pc.quark.cn" + value
	}
	if strings.HasPrefix(lower, "1/clouddrive/") {
		return "https://drive-pc.quark.cn/" + value
	}
	if strings.HasPrefix(lower, "drive-pc.quark.cn/") ||
		strings.HasPrefix(lower, "drive-h.quark.cn/") ||
		strings.HasPrefix(lower, "image.quark.cn/") ||
		strings.Contains(lower, ".drive.quark.cn/") ||
		strings.Contains(lower, ".quark.cn/") {
		return "https://" + value
	}
	return value
}

func normalizeUCPreviewURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "&amp;", "&")
	value = normalizeBrokenURLScheme(value)
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") {
		return value
	}
	if strings.HasPrefix(value, "/") {
		return "https://drive-pc.quark.cn" + value
	}
	if strings.HasPrefix(lower, "1/clouddrive/") {
		return "https://drive-pc.quark.cn/" + value
	}
	if strings.HasPrefix(lower, "pc-api.uc.cn/") ||
		strings.HasPrefix(lower, "drive.uc.cn/") ||
		strings.HasPrefix(lower, "image.uc.cn/") ||
		strings.HasPrefix(lower, "drive-pc.quark.cn/") ||
		strings.HasPrefix(lower, "drive-h.quark.cn/") ||
		strings.HasPrefix(lower, "image.quark.cn/") ||
		strings.Contains(lower, ".uc.cn/") ||
		strings.Contains(lower, ".quark.cn/") {
		return "https://" + value
	}
	return value
}

func normalizeBrokenURLScheme(value string) string {
	lower := strings.ToLower(value)
	for _, outer := range []string{"https://", "http://"} {
		if !strings.HasPrefix(lower, outer) {
			continue
		}
		rest := value[len(outer):]
		restLower := lower[len(outer):]
		for _, inner := range []string{"https", "http"} {
			validPrefix := inner + "://"
			if strings.HasPrefix(restLower, validPrefix) {
				return inner + "://" + strings.TrimLeft(rest[len(validPrefix):], "/")
			}
			noColonPrefix := inner + "//"
			if strings.HasPrefix(restLower, noColonPrefix) {
				return inner + "://" + strings.TrimLeft(rest[len(noColonPrefix):], "/")
			}
			oneSlashPrefix := inner + ":/"
			if strings.HasPrefix(restLower, oneSlashPrefix) && !strings.HasPrefix(restLower, validPrefix) {
				return inner + "://" + strings.TrimLeft(rest[len(oneSlashPrefix):], "/")
			}
			tooManySlashPrefix := inner + ":///"
			if strings.HasPrefix(restLower, tooManySlashPrefix) {
				return inner + "://" + strings.TrimLeft(rest[len(validPrefix):], "/")
			}
		}
	}
	for _, scheme := range []string{"https", "http"} {
		noColonPrefix := scheme + "//"
		if strings.HasPrefix(lower, noColonPrefix) {
			return scheme + "://" + strings.TrimLeft(value[len(noColonPrefix):], "/")
		}
		oneSlashPrefix := scheme + ":/"
		validPrefix := scheme + "://"
		if strings.HasPrefix(lower, oneSlashPrefix) && !strings.HasPrefix(lower, validPrefix) {
			return scheme + "://" + strings.TrimLeft(value[len(oneSlashPrefix):], "/")
		}
		tooManySlashPrefix := scheme + ":///"
		if strings.HasPrefix(lower, tooManySlashPrefix) {
			return scheme + "://" + strings.TrimLeft(value[len(validPrefix):], "/")
		}
	}
	return value
}

func normalizeListPage(page, pageSize, defaultSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = defaultSize
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	return page, pageSize
}

func hasMoreListPage(total, page, pageSize, currentCount int) bool {
	if total > 0 {
		return page*pageSize < total
	}
	return currentCount >= pageSize
}

func rawMapFromJSON(raw json.RawMessage) map[string]any {
	values := make(map[string]any)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &values)
	}
	return values
}

func rawListLength(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case []string:
		return len(typed)
	case []int:
		return len(typed)
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0
		}
		var values []any
		if err := json.Unmarshal([]byte(text), &values); err == nil {
			return len(values)
		}
		return 1
	}
	return 0
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroAny(values ...any) any {
	for _, value := range values {
		if numericValue(value) != 0 || boolField(value) || strings.TrimSpace(stringField(map[string]any{"value": value}, "value")) != "" {
			return value
		}
	}
	return nil
}

func normalizeBaiduShareURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = normalizeBrokenURLScheme(value)
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return value
	}
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	if strings.HasPrefix(value, "1") {
		return "https://pan.baidu.com/s/" + value
	}
	if strings.HasPrefix(value, "/s/") {
		return "https://pan.baidu.com" + value
	}
	return value
}

func stringField(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(stringifyID(value))
}

func firstNonEmptyMeaningful(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		lower := strings.ToLower(value)
		if lower == "0" || lower == "null" || lower == "undefined" || lower == "none" || value == "-" || value == "--" {
			continue
		}
		return value
	}
	return ""
}

func boolField(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	}
	return false
}

func numericValue(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
			return parsed
		}
	}
	return 0
}

func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	const maxUnixSeconds = 253402300799
	for value > maxUnixSeconds {
		value /= 1000
	}
	return time.Unix(value, 0).UTC()
}

func baiduShareExpiredTime(value int64, raw map[string]any) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	if value >= 946684800 {
		return unixTime(value)
	}
	createdAt := unixTime(firstNonZeroInt64(
		numericValue(raw["ctime"]),
		numericValue(raw["create_time"]),
		numericValue(raw["created_at"]),
	))
	if createdAt.IsZero() {
		return time.Time{}
	}
	if value <= 3650 {
		return createdAt.Add(time.Duration(value) * 24 * time.Hour)
	}
	return createdAt.Add(time.Duration(value) * time.Second)
}

func buildChildPath(basePath, name string) string {
	base := strings.TrimRight(strings.TrimSpace(basePath), "/")
	if base == "" {
		base = "/"
	}
	if base == "/" {
		return "/" + strings.TrimLeft(name, "/")
	}
	return base + "/" + strings.TrimLeft(name, "/")
}

func netdiskBaseName(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if path == "" {
		return ""
	}
	index := strings.LastIndex(path, "/")
	if index >= 0 {
		return path[index+1:]
	}
	return path
}
