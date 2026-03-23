package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	mrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultBaseURL         = "https://ilinkai.weixin.qq.com"
	defaultCDNBaseURL      = "https://novac2c.cdn.weixin.qq.com/c2c"
	defaultChannelVersion  = "1.0.2"
	defaultLongPollTimeout = 35 * time.Second
	defaultHTTPTimeout     = 15 * time.Second
	defaultConfigTimeout   = 10 * time.Second
	sessionExpiredErrCode  = -14
	typingStatusTyping     = 1
	typingStatusCancel     = 2
	mediaTypeImage         = 1
	messageTypeUser        = 1
	messageTypeBot         = 2
	messageStateFinish     = 2
	itemTypeText           = 1
	itemTypeImage          = 2
)

type Client struct {
	BaseURL        string
	CDNBaseURL     string
	Token          string
	ChannelVersion string
	RouteTag       string
	HTTPClient     *http.Client
	StateFile      string
	state          State
}

type State struct {
	AccountID     string            `json:"account_id,omitempty"`
	UserID        string            `json:"user_id,omitempty"`
	BaseURL       string            `json:"base_url,omitempty"`
	Token         string            `json:"token,omitempty"`
	GetUpdatesBuf string            `json:"get_updates_buf,omitempty"`
	ContextTokens map[string]string `json:"context_tokens,omitempty"`
}

type BaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
}

type QRCodeResponse struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

type QRStatusResponse struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token"`
	IlinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl"`
	IlinkUserID string `json:"ilink_user_id"`
}

type GetUpdatesRequest struct {
	GetUpdatesBuf string   `json:"get_updates_buf,omitempty"`
	BaseInfo      BaseInfo `json:"base_info"`
}

type GetUpdatesResponse struct {
	Ret                int             `json:"ret,omitempty"`
	ErrCode            int             `json:"errcode,omitempty"`
	ErrMsg             string          `json:"errmsg,omitempty"`
	Msgs               []WeixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf      string          `json:"get_updates_buf,omitempty"`
	LongPollingTimeout int             `json:"longpolling_timeout_ms,omitempty"`
}

type WeixinMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMS int64         `json:"create_time_ms,omitempty"`
	UpdateTimeMS int64         `json:"update_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}

type MessageItem struct {
	Type      int        `json:"type,omitempty"`
	TextItem  *TextItem  `json:"text_item,omitempty"`
	ImageItem *ImageItem `json:"image_item,omitempty"`
}

type TextItem struct {
	Text string `json:"text,omitempty"`
}

type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
}

type ImageItem struct {
	Media   *CDNMedia `json:"media,omitempty"`
	MidSize int       `json:"mid_size,omitempty"`
	AESKey  string    `json:"aeskey,omitempty"`
}

type SendMessageRequest struct {
	Msg      WeixinMessage `json:"msg"`
	BaseInfo BaseInfo      `json:"base_info"`
}

type GetUploadURLRequest struct {
	FileKey     string   `json:"filekey,omitempty"`
	MediaType   int      `json:"media_type,omitempty"`
	ToUserID    string   `json:"to_user_id,omitempty"`
	RawSize     int      `json:"rawsize,omitempty"`
	RawFileMD5  string   `json:"rawfilemd5,omitempty"`
	FileSize    int      `json:"filesize,omitempty"`
	NoNeedThumb bool     `json:"no_need_thumb,omitempty"`
	AESKey      string   `json:"aeskey,omitempty"`
	BaseInfo    BaseInfo `json:"base_info"`
}

type GetUploadURLResponse struct {
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
}

type GetConfigRequest struct {
	IlinkUserID  string   `json:"ilink_user_id,omitempty"`
	ContextToken string   `json:"context_token,omitempty"`
	BaseInfo     BaseInfo `json:"base_info"`
}

type GetConfigResponse struct {
	Ret          int    `json:"ret,omitempty"`
	ErrMsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}

type SendTypingRequest struct {
	IlinkUserID  string   `json:"ilink_user_id,omitempty"`
	TypingTicket string   `json:"typing_ticket,omitempty"`
	Status       int      `json:"status,omitempty"`
	BaseInfo     BaseInfo `json:"base_info"`
}

type UploadedMedia struct {
	FileKey                     string
	DownloadEncryptedQueryParam string
	AESKeyRaw                   []byte
	PlainSize                   int
	CipherSize                  int
}

func NewClientFromEnv() (*Client, error) {
	stateFile := getenv("WEIXIN_STATE_FILE", filepath.Join(".", "examples", "go", "weixin-state.json"))
	c := &Client{
		BaseURL:        getenv("WEIXIN_BASE_URL", defaultBaseURL),
		CDNBaseURL:     getenv("WEIXIN_CDN_BASE_URL", defaultCDNBaseURL),
		Token:          strings.TrimSpace(os.Getenv("WEIXIN_BOT_TOKEN")),
		ChannelVersion: defaultChannelVersion,
		RouteTag:       strings.TrimSpace(os.Getenv("WEIXIN_ROUTE_TAG")),
		HTTPClient:     &http.Client{Timeout: defaultHTTPTimeout},
		StateFile:      stateFile,
		state:          State{ContextTokens: map[string]string{}},
	}
	_ = c.LoadState()
	if c.state.BaseURL != "" {
		c.BaseURL = c.state.BaseURL
	}
	if c.state.Token != "" && c.Token == "" {
		c.Token = c.state.Token
	}
	return c, nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	client, err := NewClientFromEnv()
	must(err)
	ctx := context.Background()

	switch os.Args[1] {
	case "start-qr":
		qr, err := client.StartQRCodeLogin(ctx, getenv("WEIXIN_BOT_TYPE", "3"))
		must(err)
		fmt.Printf("qrcode=%s\nqrcode_url=%s\n", qr.QRCode, qr.QRCodeImgContent)
	case "wait-qr":
		if len(os.Args) < 3 {
			log.Fatal("usage: wait-qr <qrcode>")
		}
		status, err := client.WaitQRCodeLogin(ctx, os.Args[2], 8*time.Minute, getenv("WEIXIN_BOT_TYPE", "3"))
		must(err)
		fmt.Printf("status=%s bot_id=%s user_id=%s\n", status.Status, status.IlinkBotID, status.IlinkUserID)
	case "poll":
		must(client.PollForever(ctx))
	case "send-text":
		if len(os.Args) < 4 {
			log.Fatal("usage: send-text <to_user_id> <text>")
		}
		must(client.SendText(ctx, os.Args[2], strings.Join(os.Args[3:], " ")))
	case "send-image":
		if len(os.Args) < 4 {
			log.Fatal("usage: send-image <to_user_id> <file_path> [caption]")
		}
		caption := ""
		if len(os.Args) > 4 {
			caption = strings.Join(os.Args[4:], " ")
		}
		must(client.SendImage(ctx, os.Args[2], os.Args[3], caption))
	case "typing-on":
		if len(os.Args) < 3 {
			log.Fatal("usage: typing-on <to_user_id>")
		}
		must(client.SendTypingStatus(ctx, os.Args[2], typingStatusTyping))
	case "typing-off":
		if len(os.Args) < 3 {
			log.Fatal("usage: typing-off <to_user_id>")
		}
		must(client.SendTypingStatus(ctx, os.Args[2], typingStatusCancel))
	case "download-image":
		if len(os.Args) < 4 {
			log.Fatal("usage: download-image <encrypt_query_param> <aes_key_base64_or_hex>")
		}
		buf, err := client.DownloadAndDecryptMedia(ctx, os.Args[2], os.Args[3])
		must(err)
		_, _ = os.Stdout.Write(buf)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Usage:
  go run . start-qr
  go run . wait-qr <qrcode>
  go run . poll
  go run . send-text <to_user_id> <text>
  go run . send-image <to_user_id> <file_path> [caption]
  go run . typing-on <to_user_id>
  go run . typing-off <to_user_id>
  go run . download-image <encrypt_query_param> <aes_key_base64_or_hex>`)
}

func (c *Client) StartQRCodeLogin(ctx context.Context, botType string) (*QRCodeResponse, error) {
	u, _ := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/ilink/bot/get_bot_qrcode")
	q := u.Query()
	q.Set("bot_type", botType)
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if c.RouteTag != "" {
		req.Header.Set("SKRouteTag", c.RouteTag)
	}

	var out QRCodeResponse
	if err := c.doJSON(req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) WaitQRCodeLogin(ctx context.Context, qrcode string, timeout time.Duration, botType string) (*QRStatusResponse, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := c.GetQRCodeStatus(ctx, qrcode)
		if err != nil {
			return nil, err
		}
		switch status.Status {
		case "wait":
			time.Sleep(time.Second)
		case "scaned":
			log.Println("二维码已扫码，等待确认")
			time.Sleep(time.Second)
		case "expired":
			log.Println("二维码已过期，正在刷新")
			qr, err := c.StartQRCodeLogin(ctx, botType)
			if err != nil {
				return nil, err
			}
			qrcode = qr.QRCode
			fmt.Printf("new_qrcode=%s\nnew_qrcode_url=%s\n", qr.QRCode, qr.QRCodeImgContent)
		case "confirmed":
			if status.BotToken == "" {
				return nil, errors.New("confirmed but bot_token empty")
			}
			c.Token = status.BotToken
			c.state.Token = status.BotToken
			c.state.AccountID = status.IlinkBotID
			c.state.UserID = status.IlinkUserID
			if status.BaseURL != "" {
				c.BaseURL = status.BaseURL
				c.state.BaseURL = status.BaseURL
			}
			if c.state.ContextTokens == nil {
				c.state.ContextTokens = map[string]string{}
			}
			if err := c.SaveState(); err != nil {
				return nil, err
			}
			return status, nil
		default:
			return nil, fmt.Errorf("unexpected qr status: %s", status.Status)
		}
	}
	return nil, errors.New("wait qrcode login timeout")
}

func (c *Client) GetQRCodeStatus(ctx context.Context, qrcode string) (*QRStatusResponse, error) {
	u, _ := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/ilink/bot/get_qrcode_status")
	q := u.Query()
	q.Set("qrcode", qrcode)
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("iLink-App-ClientVersion", "1")
	if c.RouteTag != "" {
		req.Header.Set("SKRouteTag", c.RouteTag)
	}

	httpClient := *c.HTTPClient
	httpClient.Timeout = defaultLongPollTimeout
	resp, err := httpClient.Do(req)
	if err != nil {
		if os.IsTimeout(err) {
			return &QRStatusResponse{Status: "wait"}, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get_qrcode_status %d: %s", resp.StatusCode, string(body))
	}
	var out QRStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PollForever(ctx context.Context) error {
	if c.Token == "" {
		return errors.New("WEIXIN_BOT_TOKEN or saved state token is required")
	}
	timeout := defaultLongPollTimeout
	for {
		resp, err := c.GetUpdates(ctx, timeout)
		if err != nil {
			return err
		}
		if resp.LongPollingTimeout > 0 {
			timeout = time.Duration(resp.LongPollingTimeout) * time.Millisecond
		}
		if resp.ErrCode == sessionExpiredErrCode || resp.Ret == sessionExpiredErrCode {
			return fmt.Errorf("session expired (errcode=%d)", sessionExpiredErrCode)
		}
		if resp.Ret != 0 || resp.ErrCode != 0 {
			return fmt.Errorf("getupdates failed: ret=%d errcode=%d errmsg=%s", resp.Ret, resp.ErrCode, resp.ErrMsg)
		}
		if resp.GetUpdatesBuf != "" && resp.GetUpdatesBuf != c.state.GetUpdatesBuf {
			c.state.GetUpdatesBuf = resp.GetUpdatesBuf
			if err := c.SaveState(); err != nil {
				return err
			}
		}
		for _, msg := range resp.Msgs {
			if msg.ContextToken != "" && msg.FromUserID != "" {
				c.cacheContextToken(msg.FromUserID, msg.ContextToken)
			}
			body := extractText(msg)
			log.Printf("from=%s type=%v text=%q context_token=%t\n", msg.FromUserID, itemTypes(msg.ItemList), body, msg.ContextToken != "")
		}
	}
}

func (c *Client) GetUpdates(ctx context.Context, timeout time.Duration) (*GetUpdatesResponse, error) {
	reqBody := GetUpdatesRequest{
		GetUpdatesBuf: c.state.GetUpdatesBuf,
		BaseInfo:      c.baseInfo(),
	}
	var out GetUpdatesResponse
	err := c.postJSONWithTimeout(ctx, "/ilink/bot/getupdates", reqBody, &out, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			return &GetUpdatesResponse{Ret: 0, Msgs: nil, GetUpdatesBuf: c.state.GetUpdatesBuf}, nil
		}
		return nil, err
	}
	return &out, nil
}

func (c *Client) SendText(ctx context.Context, toUserID, text string) error {
	token := c.contextTokenFor(toUserID)
	if token == "" {
		return fmt.Errorf("missing context_token for %s; poll first or set state file", toUserID)
	}
	req := SendMessageRequest{
		Msg: WeixinMessage{
			FromUserID:   "",
			ToUserID:     toUserID,
			ClientID:     generateClientID(),
			MessageType:  messageTypeBot,
			MessageState: messageStateFinish,
			ContextToken: token,
			ItemList: []MessageItem{{
				Type:     itemTypeText,
				TextItem: &TextItem{Text: text},
			}},
		},
		BaseInfo: c.baseInfo(),
	}
	return c.postJSON(ctx, "/ilink/bot/sendmessage", req, nil)
}

func (c *Client) SendImage(ctx context.Context, toUserID, filePath, caption string) error {
	token := c.contextTokenFor(toUserID)
	if token == "" {
		return fmt.Errorf("missing context_token for %s; poll first or set state file", toUserID)
	}
	uploaded, err := c.UploadImage(ctx, toUserID, filePath)
	if err != nil {
		return err
	}
	if caption != "" {
		if err := c.SendText(ctx, toUserID, caption); err != nil {
			return err
		}
	}
	req := SendMessageRequest{
		Msg: WeixinMessage{
			ToUserID:     toUserID,
			ClientID:     generateClientID(),
			MessageType:  messageTypeBot,
			MessageState: messageStateFinish,
			ContextToken: token,
			ItemList: []MessageItem{{
				Type: itemTypeImage,
				ImageItem: &ImageItem{
					Media: &CDNMedia{
						EncryptQueryParam: uploaded.DownloadEncryptedQueryParam,
						AESKey:            base64.StdEncoding.EncodeToString(uploaded.AESKeyRaw),
						EncryptType:       1,
					},
					MidSize: uploaded.CipherSize,
				},
			}},
		},
		BaseInfo: c.baseInfo(),
	}
	return c.postJSON(ctx, "/ilink/bot/sendmessage", req, nil)
}

func (c *Client) UploadImage(ctx context.Context, toUserID, filePath string) (*UploadedMedia, error) {
	plaintext, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	aesKey := randomBytes(16)
	fileKey := hex.EncodeToString(randomBytes(16))
	cipherText, err := encryptAESECB(plaintext, aesKey)
	if err != nil {
		return nil, err
	}
	req := GetUploadURLRequest{
		FileKey:     fileKey,
		MediaType:   mediaTypeImage,
		ToUserID:    toUserID,
		RawSize:     len(plaintext),
		RawFileMD5:  hex.EncodeToString(md5sum(plaintext)),
		FileSize:    len(cipherText),
		NoNeedThumb: true,
		AESKey:      hex.EncodeToString(aesKey),
		BaseInfo:    c.baseInfo(),
	}
	var resp GetUploadURLResponse
	if err := c.postJSON(ctx, "/ilink/bot/getuploadurl", req, &resp); err != nil {
		return nil, err
	}
	if resp.UploadParam == "" {
		return nil, errors.New("getuploadurl returned empty upload_param")
	}
	downloadParam, err := c.uploadCipherToCDN(ctx, resp.UploadParam, fileKey, cipherText)
	if err != nil {
		return nil, err
	}
	return &UploadedMedia{
		FileKey:                     fileKey,
		DownloadEncryptedQueryParam: downloadParam,
		AESKeyRaw:                   aesKey,
		PlainSize:                   len(plaintext),
		CipherSize:                  len(cipherText),
	}, nil
}

func (c *Client) DownloadAndDecryptMedia(ctx context.Context, encryptQueryParam, aesKeyBase64OrHex string) ([]byte, error) {
	u := fmt.Sprintf("%s/download?encrypted_query_param=%s", strings.TrimRight(c.CDNBaseURL, "/"), url.QueryEscape(encryptQueryParam))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cdn download %d: %s", resp.StatusCode, string(body))
	}
	ciphertext, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	key, err := decodeMediaAESKey(aesKeyBase64OrHex)
	if err != nil {
		return nil, err
	}
	return decryptAESECB(ciphertext, key)
}

func (c *Client) SendTypingStatus(ctx context.Context, toUserID string, status int) error {
	token := c.contextTokenFor(toUserID)
	if token == "" {
		return fmt.Errorf("missing context_token for %s", toUserID)
	}
	var cfgResp GetConfigResponse
	if err := c.postJSON(ctx, "/ilink/bot/getconfig", GetConfigRequest{
		IlinkUserID:  toUserID,
		ContextToken: token,
		BaseInfo:     c.baseInfo(),
	}, &cfgResp); err != nil {
		return err
	}
	if cfgResp.Ret != 0 {
		return fmt.Errorf("getconfig failed: %s", cfgResp.ErrMsg)
	}
	return c.postJSONWithTimeout(ctx, "/ilink/bot/sendtyping", SendTypingRequest{
		IlinkUserID:  toUserID,
		TypingTicket: cfgResp.TypingTicket,
		Status:       status,
		BaseInfo:     c.baseInfo(),
	}, nil, defaultConfigTimeout)
}

func (c *Client) uploadCipherToCDN(ctx context.Context, uploadParam, fileKey string, cipherText []byte) (string, error) {
	u := fmt.Sprintf("%s/upload?encrypted_query_param=%s&filekey=%s", strings.TrimRight(c.CDNBaseURL, "/"), url.QueryEscape(uploadParam), url.QueryEscape(fileKey))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(cipherText))
	req.Header.Set("Content-Type", "application/octet-stream")
	var lastErr error
	for i := 0; i < 3; i++ {
		resp, err := c.HTTPClient.Do(req.Clone(ctx))
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return "", fmt.Errorf("cdn upload client error: %d", resp.StatusCode)
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("cdn upload server error: %d", resp.StatusCode)
			continue
		}
		downloadParam := resp.Header.Get("x-encrypted-param")
		if downloadParam == "" {
			return "", errors.New("cdn upload missing x-encrypted-param")
		}
		return downloadParam, nil
	}
	return "", lastErr
}

func (c *Client) postJSON(ctx context.Context, endpoint string, payload any, out any) error {
	return c.postJSONWithTimeout(ctx, endpoint, payload, out, defaultHTTPTimeout)
}

func (c *Client) postJSONWithTimeout(ctx context.Context, endpoint string, payload any, out any, timeout time.Duration) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+endpoint, bytes.NewReader(body))
	c.applyAPIHeaders(req, body)

	httpClient := *c.HTTPClient
	httpClient.Timeout = timeout
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %d: %s", endpoint, resp.StatusCode, string(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %d: %s", req.URL.Path, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) applyAPIHeaders(req *http.Request, body []byte) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Content-Length", fmt.Sprint(len(body)))
	req.Header.Set("X-WECHAT-UIN", buildRandomWechatUIN())
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.RouteTag != "" {
		req.Header.Set("SKRouteTag", c.RouteTag)
	}
}

func (c *Client) baseInfo() BaseInfo {
	return BaseInfo{ChannelVersion: c.ChannelVersion}
}

func (c *Client) cacheContextToken(userID, token string) {
	if c.state.ContextTokens == nil {
		c.state.ContextTokens = map[string]string{}
	}
	c.state.ContextTokens[userID] = token
	_ = c.SaveState()
}

func (c *Client) contextTokenFor(userID string) string {
	if c.state.ContextTokens == nil {
		return ""
	}
	return c.state.ContextTokens[userID]
}

func (c *Client) LoadState() error {
	data, err := os.ReadFile(c.StateFile)
	if err != nil {
		return nil
	}
	return json.Unmarshal(data, &c.state)
}

func (c *Client) SaveState() error {
	if c.StateFile == "" {
		return nil
	}
	if c.state.BaseURL == "" {
		c.state.BaseURL = c.BaseURL
	}
	if c.state.Token == "" {
		c.state.Token = c.Token
	}
	if c.state.ContextTokens == nil {
		c.state.ContextTokens = map[string]string{}
	}
	if err := os.MkdirAll(filepath.Dir(c.StateFile), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.StateFile, body, 0o600)
}

func extractText(msg WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == itemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func itemTypes(items []MessageItem) []int {
	out := make([]int, 0, len(items))
	for _, item := range items {
		out = append(out, item.Type)
	}
	return out
}

func buildRandomWechatUIN() string {
	n := mrand.Uint32()
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprint(n)))
}

func generateClientID() string {
	return fmt.Sprintf("openclaw-weixin-%d", time.Now().UnixNano())
}

func randomBytes(n int) []byte {
	buf := make([]byte, n)
	_, err := rand.Read(buf)
	must(err)
	return buf
}

func md5sum(buf []byte) []byte {
	sum := md5.Sum(buf)
	return sum[:]
}

func encryptAESECB(plaintext, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("aes key must be 16 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plaintext, block.BlockSize())
	out := make([]byte, len(padded))
	for bs := 0; bs < len(padded); bs += block.BlockSize() {
		block.Encrypt(out[bs:bs+block.BlockSize()], padded[bs:bs+block.BlockSize()])
	}
	return out, nil
}

func decryptAESECB(ciphertext, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("aes key must be 16 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext)%block.BlockSize() != 0 {
		return nil, errors.New("ciphertext is not a multiple of block size")
	}
	out := make([]byte, len(ciphertext))
	for bs := 0; bs < len(ciphertext); bs += block.BlockSize() {
		block.Decrypt(out[bs:bs+block.BlockSize()], ciphertext[bs:bs+block.BlockSize()])
	}
	return pkcs7Unpad(out, block.BlockSize())
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padLen := blockSize - (len(data) % blockSize)
	if padLen == 0 {
		padLen = blockSize
	}
	return append(data, bytes.Repeat([]byte{byte(padLen)}, padLen)...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid padded data length")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize || padLen > len(data) {
		return nil, errors.New("invalid padding")
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return nil, errors.New("invalid padding content")
		}
	}
	return data[:len(data)-padLen], nil
}

func decodeMediaAESKey(v string) ([]byte, error) {
	if raw, err := hex.DecodeString(v); err == nil && len(raw) == 16 {
		return raw, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, err
	}
	if len(decoded) == 16 {
		return decoded, nil
	}
	if len(decoded) == 32 {
		if raw, err := hex.DecodeString(string(decoded)); err == nil && len(raw) == 16 {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("unsupported aes key encoding, decoded len=%d", len(decoded))
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

var _ = cipher.Block(nil)
var _ = math.MaxInt
