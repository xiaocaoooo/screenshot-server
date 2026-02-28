package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/gin-gonic/gin"
)

const (
	defaultWidth       = 1920
	defaultHeight      = 1080
	defaultFormat      = "png"
	defaultQuality     = 90
	defaultDeviceScale = 1.0
	defaultTimeoutSec  = 30
	maxTimeoutSec      = 120

	// maxAutoViewportHeight 用于“未显式设置 height + 元素截图”时自动把视口高度扩展到页面总高度。
	// 该值是安全阈值，避免极端超长页面导致过高的内存/时间开销。
	maxAutoViewportHeight = 30000

	// remoteChromeDialTimeout 控制“连接远程 Chrome DevTools WebSocket（dial）”阶段的独立超时。
	// 注意：该超时仅用于首次建立 CDP 连接（握手/建立 session），后续 Navigate/Wait/Screenshot 仍使用请求整体 timeout。
	remoteChromeDialTimeout = 30 * time.Second

	defaultBrowserlessHTTPURL = "http://localhost:3000"
)

type Clip struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type ScreenshotRequest struct {
	URL         string            `json:"url"`
	Selector    string            `json:"selector"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	Format      string            `json:"format"`
	Quality     int               `json:"quality"`
	WaitTime    int               `json:"wait_time"`
	WaitFor     string            `json:"wait_for"`
	FullPage    bool              `json:"full_page"`
	Headers     map[string]string `json:"headers"`
	UserAgent   string            `json:"user_agent"`
	DeviceScale float64           `json:"device_scale"`
	Mobile      bool              `json:"mobile"`
	Landscape   bool              `json:"landscape"`
	Timeout     int               `json:"timeout"`
	Clip        *Clip             `json:"clip"`
}

func (r *ScreenshotRequest) applyDefaults() {
	if r.Width == 0 {
		r.Width = defaultWidth
	}
	// 对于元素截图：如果用户未设置 height（==0），后续会在截图前自动扩展为页面总高度。
	if r.Height == 0 && r.Selector == "" {
		r.Height = defaultHeight
	}
	if r.Format == "" {
		r.Format = defaultFormat
	}
	if r.Quality == 0 {
		r.Quality = defaultQuality
	}
	if r.DeviceScale == 0 {
		r.DeviceScale = defaultDeviceScale
	}
	if r.Timeout == 0 {
		r.Timeout = defaultTimeoutSec
	}
}

func (r *ScreenshotRequest) validate() error {
	if r.URL == "" {
		return errors.New("url is required")
	}

	parsedURL, err := url.ParseRequestURI(r.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return errors.New("url must be a valid http/https URL")
	}

	if r.Width < 100 || r.Width > 4096 {
		return errors.New("width must be between 100 and 4096")
	}
	// height 允许为 0：仅在“元素截图且未设置 height”时使用，后续会自动扩展为页面总高度。
	if r.Height != 0 {
		if r.Height < 100 || r.Height > 10000 {
			return errors.New("height must be between 100 and 10000")
		}
	} else if r.Selector == "" {
		return errors.New("height must be between 100 and 10000")
	}

	f := strings.ToLower(r.Format)
	if f != "png" && f != "jpeg" && f != "webp" {
		return errors.New("format must be one of: png, jpeg, webp")
	}
	r.Format = f

	if r.Quality < 1 || r.Quality > 100 {
		return errors.New("quality must be between 1 and 100")
	}

	if r.Timeout < 1 || r.Timeout > maxTimeoutSec {
		return fmt.Errorf("timeout must be between 1 and %d seconds", maxTimeoutSec)
	}

	if r.DeviceScale <= 0 || r.DeviceScale > 4 {
		return errors.New("device_scale must be between 0 and 4")
	}

	if r.WaitTime < 0 {
		return errors.New("wait_time must be >= 0")
	}

	if r.Clip != nil {
		if r.Clip.Width <= 0 || r.Clip.Height <= 0 {
			return errors.New("clip width/height must be > 0")
		}
		if r.Clip.X < 0 || r.Clip.Y < 0 {
			return errors.New("clip x/y must be >= 0")
		}
	}

	return nil
}

func parseBoolQuery(c *gin.Context, key string, defaultValue bool) (bool, error) {
	v := c.Query(key)
	if v == "" {
		return defaultValue, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be boolean", key)
	}
	return b, nil
}

func parseIntQuery(c *gin.Context, key string, defaultValue int) (int, error) {
	v := c.Query(key)
	if v == "" {
		return defaultValue, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be integer", key)
	}
	return i, nil
}

func parseFloatQuery(c *gin.Context, key string, defaultValue float64) (float64, error) {
	v := c.Query(key)
	if v == "" {
		return defaultValue, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be number", key)
	}
	return f, nil
}

func parseRequestFromGET(c *gin.Context) (ScreenshotRequest, error) {
	req := ScreenshotRequest{
		URL:      c.Query("url"),
		Selector: c.Query("selector"),
		Format:   c.DefaultQuery("format", defaultFormat),
		WaitFor:  c.Query("wait_for"),
	}

	var err error
	req.Width, err = parseIntQuery(c, "width", defaultWidth)
	if err != nil {
		return req, err
	}
	// height：GET 场景下如果未提供，则保持为 0（元素截图会在截图前自动扩展总高度；非元素截图会在 applyDefaults 中补默认值）。
	req.Height, err = parseIntQuery(c, "height", 0)
	if err != nil {
		return req, err
	}
	req.Quality, err = parseIntQuery(c, "quality", defaultQuality)
	if err != nil {
		return req, err
	}
	req.WaitTime, err = parseIntQuery(c, "wait_time", 0)
	if err != nil {
		return req, err
	}
	req.Timeout, err = parseIntQuery(c, "timeout", defaultTimeoutSec)
	if err != nil {
		return req, err
	}
	req.DeviceScale, err = parseFloatQuery(c, "device_scale", defaultDeviceScale)
	if err != nil {
		return req, err
	}
	req.FullPage, err = parseBoolQuery(c, "full_page", false)
	if err != nil {
		return req, err
	}
	req.Mobile, err = parseBoolQuery(c, "mobile", false)
	if err != nil {
		return req, err
	}
	req.Landscape, err = parseBoolQuery(c, "landscape", false)
	if err != nil {
		return req, err
	}

	req.UserAgent = c.Query("user_agent")

	headersRaw := c.Query("headers")
	if headersRaw != "" {
		headers := map[string]string{}
		if err := json.Unmarshal([]byte(headersRaw), &headers); err != nil {
			return req, errors.New("headers must be a valid JSON object")
		}
		req.Headers = headers
	}

	return req, nil
}

func parseRequest(c *gin.Context) (ScreenshotRequest, error) {
	if c.Request.Method == http.MethodGet {
		return parseRequestFromGET(c)
	}

	var req ScreenshotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return req, errors.New("invalid JSON body")
	}
	req.applyDefaults()
	return req, nil
}

type browserlessVersionResponse struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type browserlessCDPJSONPayload struct {
	Description          string `json:"description"`
	DevtoolsFrontendURL  string `json:"devtoolsFrontendUrl"`
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func hasDevToolsPath(wsRaw string) bool {
	wsRaw = strings.TrimSpace(wsRaw)
	if wsRaw == "" {
		return false
	}
	u, err := url.Parse(wsRaw)
	if err != nil {
		return false
	}
	p := strings.TrimSpace(u.Path)
	// browser endpoint 常见是 /devtools/browser/<id>，page endpoint 常见是 /devtools/page/<id>
	return strings.HasPrefix(p, "/devtools/")
}

func getBrowserlessHTTPURL() string {
	// 默认固定/指向本机 25004
	v, ok := os.LookupEnv("BROWSERLESS_HTTP_URL")
	if !ok {
		return defaultBrowserlessHTTPURL
	}
	return strings.TrimSpace(v)
}

func getChromeWSEndpoint() string {
	return strings.TrimSpace(os.Getenv("CHROME_WS_ENDPOINT"))
}

func parseBrowserlessHTTPBase(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("BROWSERLESS_HTTP_URL is empty")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid BROWSERLESS_HTTP_URL %q: %w", raw, err)
	}
	if u.Scheme == "" {
		return nil, fmt.Errorf("invalid BROWSERLESS_HTTP_URL %q: missing scheme (http/https)", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid BROWSERLESS_HTTP_URL %q: scheme must be http/https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid BROWSERLESS_HTTP_URL %q: missing host", raw)
	}
	return u, nil
}

func httpBaseHostPortWithDefault(u *url.URL) (string, error) {
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("invalid BROWSERLESS_HTTP_URL %q: missing hostname", u.String())
	}

	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", fmt.Errorf("invalid BROWSERLESS_HTTP_URL %q: unsupported scheme %q", u.String(), u.Scheme)
		}
	}

	return net.JoinHostPort(host, port), nil
}

func wsSchemeForHTTPBase(u *url.URL) (string, error) {
	switch u.Scheme {
	case "http":
		return "ws", nil
	case "https":
		return "wss", nil
	default:
		return "", fmt.Errorf("invalid BROWSERLESS_HTTP_URL %q: unsupported scheme %q", u.String(), u.Scheme)
	}
}

func rewriteWebSocketDebuggerURL(webSocketDebuggerURL string, httpBase *url.URL) (string, error) {
	wsRaw := strings.TrimSpace(webSocketDebuggerURL)
	if wsRaw == "" {
		return "", errors.New("missing webSocketDebuggerUrl")
	}

	wsU, err := url.Parse(wsRaw)
	if err != nil {
		return "", fmt.Errorf("invalid webSocketDebuggerUrl %q: %w", wsRaw, err)
	}
	if wsU.Scheme == "" || wsU.Host == "" {
		return "", fmt.Errorf("invalid webSocketDebuggerUrl %q: missing scheme or host", wsRaw)
	}

	// browserless 可能返回容器内部地址（如 ws://0.0.0.0:3000/...），这里强制用对外暴露的 BROWSERLESS_HTTP_URL 的 host:port。
	hostPort, err := httpBaseHostPortWithDefault(httpBase)
	if err != nil {
		return "", err
	}
	desiredScheme, err := wsSchemeForHTTPBase(httpBase)
	if err != nil {
		return "", err
	}

	wsU.Scheme = desiredScheme
	wsU.Host = hostPort
	return wsU.String(), nil
}

func httpBaseFromWSEndpoint(wsRaw string) (*url.URL, error) {
	wsRaw = strings.TrimSpace(wsRaw)
	if wsRaw == "" {
		return nil, errors.New("ws endpoint is empty")
	}

	u, err := url.Parse(wsRaw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("scheme must be ws/wss, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("missing host")
	}

	httpScheme := "http"
	if u.Scheme == "wss" {
		httpScheme = "https"
	}

	// 保留 path（以支持反向代理 base path），但丢弃 query/fragment。
	return &url.URL{Scheme: httpScheme, Host: u.Host, Path: u.Path}, nil
}

func resolveWSEndpointViaJSONNew(ctx context.Context, httpBase *url.URL) (string, error) {
	newURL := *httpBase
	basePath := strings.TrimRight(newURL.Path, "/")
	newURL.Path = basePath + "/json/new"
	newURL.RawQuery = ""
	newURL.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, newURL.String(), nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("browserless /json/new returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload browserlessCDPJSONPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}

	if !hasDevToolsPath(payload.WebSocketDebuggerURL) {
		return "", fmt.Errorf("browserless /json/new returned non-devtools ws: %q", strings.TrimSpace(payload.WebSocketDebuggerURL))
	}

	rewritten, err := rewriteWebSocketDebuggerURL(payload.WebSocketDebuggerURL, httpBase)
	if err != nil {
		return "", err
	}

	log.Printf("resolveWSEndpoint: resolved via /json/new raw=%q rewritten=%q", strings.TrimSpace(payload.WebSocketDebuggerURL), rewritten)
	return rewritten, nil
}

func resolveWSEndpointViaJSONList(ctx context.Context, httpBase *url.URL) (string, error) {
	listURL := *httpBase
	basePath := strings.TrimRight(listURL.Path, "/")
	listURL.Path = basePath + "/json/list"
	listURL.RawQuery = ""
	listURL.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL.String(), nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("browserless /json/list returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payloads []browserlessCDPJSONPayload
	if err := json.NewDecoder(resp.Body).Decode(&payloads); err != nil {
		return "", err
	}

	for _, p := range payloads {
		if !hasDevToolsPath(p.WebSocketDebuggerURL) {
			continue
		}

		rewritten, err := rewriteWebSocketDebuggerURL(p.WebSocketDebuggerURL, httpBase)
		if err != nil {
			continue
		}
		log.Printf("resolveWSEndpoint: resolved via /json/list raw=%q rewritten=%q", strings.TrimSpace(p.WebSocketDebuggerURL), rewritten)
		return rewritten, nil
	}

	// 兜底：方便排查，打印数量（不打印全量内容避免日志污染）
	return "", fmt.Errorf("browserless /json/list returned %d targets, but none has a usable devtools ws", len(payloads))
}

func resolveWSEndpointViaJSONVersion(ctx context.Context, httpBase *url.URL) (string, error) {
	// 构造 /json/version（保留可能存在的 base path；丢弃 query/fragment）
	versionURL := *httpBase
	basePath := strings.TrimRight(versionURL.Path, "/")
	versionURL.Path = basePath + "/json/version"
	versionURL.RawQuery = ""
	versionURL.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL.String(), nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("browserless /json/version returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var vr browserlessVersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return "", err
	}

	raw := strings.TrimSpace(vr.WebSocketDebuggerURL)
	log.Printf("resolveWSEndpoint: /json/version webSocketDebuggerUrl=%q", raw)

	// 理想情况：/json/version 直接给出 /devtools/browser/<id>
	if hasDevToolsPath(raw) {
		rewritten, err := rewriteWebSocketDebuggerURL(raw, httpBase)
		if err != nil {
			return "", err
		}
		return rewritten, nil
	}

	// 现实情况（抓包复现）：/json/version 可能只返回 ws://0.0.0.0:3000（无 /devtools/...）。
	// 这种 ws 无法 websocket upgrade（会落到 HTTP 200），必须 fallback 到 /json/new 或 /json/list 获取完整 ws。
	log.Printf("resolveWSEndpoint: /json/version ws missing /devtools path, fallback to /json/new then /json/list")

	if resolved, err := resolveWSEndpointViaJSONNew(ctx, httpBase); err == nil {
		return resolved, nil
	} else {
		log.Printf("resolveWSEndpoint: /json/new fallback failed: %v", err)
	}

	if resolved, err := resolveWSEndpointViaJSONList(ctx, httpBase); err == nil {
		return resolved, nil
	} else {
		log.Printf("resolveWSEndpoint: /json/list fallback failed: %v", err)
	}

	// 保留原始值，便于错误提示定位
	return "", fmt.Errorf("browserless /json/version returned non-devtools ws (%q) and fallbacks (/json/new,/json/list) failed", raw)
}

func resolveWSEndpoint(ctx context.Context) (wsURL string, configured bool, err error) {
	if ws := getChromeWSEndpoint(); ws != "" {
		// 兼容两种配置：
		// 1) 完整 ws（含 /devtools/browser/<id>）——直接使用
		// 2) 仅 host:port（无 devtools path）——需要通过 /json/version 解析出完整 ws
		if u, parseErr := url.Parse(ws); parseErr == nil && strings.HasPrefix(u.Path, "/devtools/browser/") {
			log.Printf("resolveWSEndpoint: using CHROME_WS_ENDPOINT (full): %s", ws)
			return ws, true, nil
		}

		httpBase, convErr := httpBaseFromWSEndpoint(ws)
		if convErr != nil {
			return "", true, fmt.Errorf("invalid CHROME_WS_ENDPOINT %q: %w", ws, convErr)
		}

		resolved, rErr := resolveWSEndpointViaJSONVersion(ctx, httpBase)
		if rErr != nil {
			return "", true, rErr
		}
		log.Printf("resolveWSEndpoint: CHROME_WS_ENDPOINT=%s resolved via /json/version -> %s", ws, resolved)
		return resolved, true, nil
	}

	httpBaseRaw := getBrowserlessHTTPURL()
	if httpBaseRaw == "" {
		return "", false, errors.New("browserless endpoint is not configured")
	}

	httpBase, err := parseBrowserlessHTTPBase(httpBaseRaw)
	if err != nil {
		return "", true, err
	}

	resolved, err := resolveWSEndpointViaJSONVersion(ctx, httpBase)
	if err != nil {
		return "", true, err
	}
	log.Printf("resolveWSEndpoint: BROWSERLESS_HTTP_URL=%s resolved via /json/version -> %s", httpBaseRaw, resolved)
	return resolved, true, nil
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || strings.Contains(strings.ToLower(err.Error()), "deadline exceeded")
}

func contentTypeForFormat(format string) string {
	switch strings.ToLower(format) {
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func captureFormat(format string) page.CaptureScreenshotFormat {
	switch strings.ToLower(format) {
	case "jpeg":
		return page.CaptureScreenshotFormatJpeg
	case "webp":
		return page.CaptureScreenshotFormatWebp
	default:
		return page.CaptureScreenshotFormatPng
	}
}

func screenshotHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		req, err := parseRequest(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		req.applyDefaults()
		if err := req.validate(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// 视口尺寸：req.Height 允许为 0（元素截图且未设置 height）。此时先用默认高度完成加载，
		// 截图前再自动扩展为页面总高度。
		viewportWidth := int64(req.Width)
		viewportHeight := int64(req.Height)
		autoExpandViewportHeight := req.Selector != "" && req.Height == 0
		if viewportHeight == 0 {
			viewportHeight = defaultHeight
		}

		if req.Mobile && req.Landscape {
			viewportWidth, viewportHeight = viewportHeight, viewportWidth
		}

		overallCtx, cancel := context.WithTimeout(context.Background(), time.Duration(req.Timeout)*time.Second)
		defer cancel()

		wsURL, configured, err := resolveWSEndpoint(overallCtx)
		if !configured {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "browserless/chrome endpoint is not configured, set BROWSERLESS_HTTP_URL or CHROME_WS_ENDPOINT"})
			return
		}
		if err != nil {
			// 解析/探测 browserless 失败属于上游不可用
			if isTimeoutErr(err) {
				c.JSON(http.StatusGatewayTimeout, gin.H{"error": "browserless endpoint timeout", "details": err.Error()})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to resolve browserless websocket endpoint", "details": err.Error()})
			return
		}

		log.Printf("screenshotHandler: using chrome ws endpoint: %s", wsURL)

		allocCtx, allocCancel := chromedp.NewRemoteAllocator(overallCtx, wsURL)
		defer allocCancel()

		taskCtx, taskCancel := chromedp.NewContext(allocCtx)
		defer taskCancel()

		// dial 阶段：用独立的 30s 超时先完成一次轻量 CDP 调用，确保 websocket/握手/首次 session 建立。
		// dial 成功后，后续所有动作仍用 taskCtx（其整体 deadline 来自请求 timeout）。
		dialCtx, dialCancel := context.WithTimeout(taskCtx, remoteChromeDialTimeout)
		defer dialCancel()

		if err := chromedp.Run(dialCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			// 只读操作，用于触发与远程 Chrome 的首次连接。
			_, err := page.GetFrameTree().Do(ctx)
			return err
		})); err != nil {
			// dialCtx 自身超时（最明确）
			if errors.Is(dialCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
				c.JSON(http.StatusGatewayTimeout, gin.H{"error": "chrome dial timeout", "details": err.Error()})
				return
			}

			// 其他 dial 类错误：尽量保持与后续 chromedp.Run 的错误码映射一致（连接/握手 => 502）
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "websocket") || strings.Contains(msg, "handshake") || strings.Contains(msg, "connect") || strings.Contains(msg, "dial") {
				c.JSON(http.StatusBadGateway, gin.H{"error": "failed to connect chrome endpoint", "details": "dial failed: " + err.Error()})
				return
			}
			if isTimeoutErr(err) {
				c.JSON(http.StatusGatewayTimeout, gin.H{"error": "chrome dial timeout", "details": err.Error()})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to connect chrome endpoint", "details": err.Error()})
			return
		}

		actions := make([]chromedp.Action, 0, 16)

		actions = append(actions,
			network.Enable(),
			emulation.SetDeviceMetricsOverride(viewportWidth, viewportHeight, req.DeviceScale, req.Mobile),
		)

		if req.UserAgent != "" {
			// cdproto 中 UA override 位于 Emulation domain
			actions = append(actions, emulation.SetUserAgentOverride(req.UserAgent))
		}

		if len(req.Headers) > 0 {
			headers := make(network.Headers, len(req.Headers))
			for k, v := range req.Headers {
				headers[k] = v
			}
			actions = append(actions, network.SetExtraHTTPHeaders(headers))
		}

		actions = append(actions,
			chromedp.Navigate(req.URL),
			chromedp.WaitReady("body", chromedp.ByQuery),
		)

		if req.WaitFor != "" {
			actions = append(actions, chromedp.WaitVisible(req.WaitFor, chromedp.ByQuery))
		}

		if req.WaitTime > 0 {
			actions = append(actions, chromedp.Sleep(time.Duration(req.WaitTime)*time.Millisecond))
		}

		// 元素截图 + 未设置 height：截图前先获取页面总高度，把视口高度扩展到页面高度。
		// 不新增参数：以 height==0 作为触发条件。
		if autoExpandViewportHeight {
			actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
				// 优先使用 LayoutMetrics（更接近渲染层的真实尺寸）
				var pageHeight float64
				if _, _, contentSize, _, _, _, err := page.GetLayoutMetrics().Do(ctx); err == nil && contentSize != nil && contentSize.Height > 0 {
					pageHeight = contentSize.Height
				} else {
					// fallback：用 DOM 的 scrollHeight
					var h float64
					js := `(() => {
						const de = document.documentElement;
						const b = document.body;
						return Math.max(
							de ? de.scrollHeight : 0,
							de ? de.offsetHeight : 0,
							b ? b.scrollHeight : 0,
							b ? b.offsetHeight : 0
						);
					})()`
					if err := chromedp.EvaluateAsDevTools(js, &h).Do(ctx); err != nil {
						return err
					}
					pageHeight = h
				}

				if pageHeight <= 0 {
					return fmt.Errorf("failed to determine page height")
				}

				desired := int64(math.Ceil(pageHeight))
				if desired < viewportHeight {
					desired = viewportHeight
				}
				if desired > maxAutoViewportHeight {
					desired = maxAutoViewportHeight
				}

				if desired != viewportHeight {
					viewportHeight = desired
					if err := emulation.SetDeviceMetricsOverride(viewportWidth, viewportHeight, req.DeviceScale, req.Mobile).Do(ctx); err != nil {
						return err
					}
				}

				// 给浏览器一点时间完成 relayout
				return nil
			}))
		}

		var clip *page.Viewport
		if req.Clip != nil {
			clip = &page.Viewport{X: req.Clip.X, Y: req.Clip.Y, Width: req.Clip.Width, Height: req.Clip.Height, Scale: 1}
		}

		// selector 截图：尽量保持与 Playwright 行为一致：滚动到元素、再计算 bounding box 并转成 clip
		if req.Selector != "" {
			actions = append(actions,
				chromedp.ScrollIntoView(req.Selector, chromedp.ByQuery),
				chromedp.WaitVisible(req.Selector, chromedp.ByQuery),
				chromedp.ActionFunc(func(ctx context.Context) error {
					js := fmt.Sprintf(`(() => {
						const el = document.querySelector(%q);
						if (!el) return null;
						const r = el.getBoundingClientRect();
						return { x: r.x + window.scrollX, y: r.y + window.scrollY, width: r.width, height: r.height };
					})()`, req.Selector)

					var rect struct {
						X      float64 `json:"x"`
						Y      float64 `json:"y"`
						Width  float64 `json:"width"`
						Height float64 `json:"height"`
					}
					if err := chromedp.EvaluateAsDevTools(js, &rect).Do(ctx); err != nil {
						return err
					}
					if rect.Width <= 0 || rect.Height <= 0 {
						return fmt.Errorf("selector resolved but has empty bounding box: %s", req.Selector)
					}
					clip = &page.Viewport{X: rect.X, Y: rect.Y, Width: rect.Width, Height: rect.Height, Scale: 1}
					return nil
				}),
			)
		} else if req.FullPage && clip == nil {
			// full_page：用 LayoutMetrics 的 contentSize 构造 clip
			actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
				_, _, contentSize, _, _, _, err := page.GetLayoutMetrics().Do(ctx)
				if err != nil {
					return err
				}
				if contentSize == nil {
					return errors.New("failed to get layout metrics content size")
				}
				if contentSize.Width <= 0 || contentSize.Height <= 0 {
					return fmt.Errorf("invalid content size: %vx%v", contentSize.Width, contentSize.Height)
				}
				clip = &page.Viewport{X: 0, Y: 0, Width: contentSize.Width, Height: contentSize.Height, Scale: 1}
				return nil
			}))
		}

		var img []byte
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			cap := page.CaptureScreenshot().WithFromSurface(true).WithFormat(captureFormat(req.Format))

			// full_page 在给出大 clip 时，最好允许越过视口捕获
			if req.FullPage && req.Selector == "" && req.Clip == nil {
				cap = cap.WithCaptureBeyondViewport(true)
			}

			if req.Format == "jpeg" || req.Format == "webp" {
				cap = cap.WithQuality(int64(req.Quality))
			}
			if clip != nil {
				cap = cap.WithClip(clip)
			}
			buf, err := cap.Do(ctx)
			if err != nil {
				return err
			}
			img = buf
			return nil
		}))

		if err := chromedp.Run(taskCtx, actions...); err != nil {
			if isTimeoutErr(err) {
				c.JSON(http.StatusGatewayTimeout, gin.H{"error": "screenshot timeout", "details": err.Error()})
				return
			}
			// 远程连接类错误（握手/不可达）尽量映射为 502
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "websocket") || strings.Contains(msg, "handshake") || strings.Contains(msg, "connect") {
				c.JSON(http.StatusBadGateway, gin.H{"error": "failed to connect chrome endpoint", "details": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to screenshot", "details": err.Error()})
			return
		}

		c.Data(http.StatusOK, contentTypeForFormat(req.Format), img)
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		// health 要求：当未配置可用 endpoint 时返回 503
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		wsURL, configured, err := resolveWSEndpoint(ctx)
		available := configured && err == nil && wsURL != ""

		status := http.StatusOK
		state := "ok"
		if !available {
			status = http.StatusServiceUnavailable
			state = "degraded"
		}

		payload := gin.H{
			"status":               state,
			"time":                 time.Now().UTC().Format(time.RFC3339),
			"chrome_ws_configured": configured,
			"chrome_ws_available":  available,
			"browserless_http_url": getBrowserlessHTTPURL(),
			"chrome_ws_endpoint":   wsURL,
		}
		if err != nil {
			payload["details"] = err.Error()
		}

		c.JSON(status, payload)
	})

	r.GET("/screenshot", screenshotHandler())
	r.POST("/screenshot", screenshotHandler())

	if err := r.Run(":" + port); err != nil {
		log.Fatalf("server start failed: %v", err)
	}
}
