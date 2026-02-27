package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	playwright "github.com/playwright-community/playwright-go"
)

const (
	defaultWidth       = 1920
	defaultHeight      = 1080
	defaultFormat      = "png"
	defaultQuality     = 90
	defaultDeviceScale = 1.0
	defaultTimeoutSec  = 30
	maxTimeoutSec      = 120
)

func ensurePlaywrightDriver() error {
	if err := playwright.Install(&playwright.RunOptions{
		SkipInstallBrowsers: true,
	}); err != nil {
		return err
	}
	return nil
}

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
	if r.Height == 0 {
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
	if r.Height < 100 || r.Height > 10000 {
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
	req.Height, err = parseIntQuery(c, "height", defaultHeight)
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

func screenshotHandler(playwrightWS string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if playwrightWS == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "playwright server is not configured, set PLAYWRIGHT_WS_ENDPOINT"})
			return
		}

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

		if req.Mobile && req.Landscape {
			req.Width, req.Height = req.Height, req.Width
		}

		pw, err := playwright.Run()
		if err != nil {
			if installErr := ensurePlaywrightDriver(); installErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize playwright", "details": err.Error(), "install_error": installErr.Error()})
				return
			}

			pw, err = playwright.Run()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize playwright", "details": err.Error()})
				return
			}
		}
		defer func() {
			_ = pw.Stop()
		}()

		browser, err := pw.Chromium.Connect(playwrightWS)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to connect playwright server", "details": err.Error()})
			return
		}
		defer func() {
			_ = browser.Close()
		}()

		newPageOptions := playwright.BrowserNewPageOptions{
			Viewport: &playwright.Size{
				Width:  req.Width,
				Height: req.Height,
			},
			DeviceScaleFactor: playwright.Float(req.DeviceScale),
			IsMobile:          playwright.Bool(req.Mobile),
		}
		if req.UserAgent != "" {
			newPageOptions.UserAgent = playwright.String(req.UserAgent)
		}

		page, err := browser.NewPage(newPageOptions)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create page", "details": err.Error()})
			return
		}
		defer func() {
			_ = page.Close()
		}()

		if len(req.Headers) > 0 {
			if err := page.SetExtraHTTPHeaders(req.Headers); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid headers", "details": err.Error()})
				return
			}
		}

		timeoutMS := float64(req.Timeout * 1000)
		if _, err := page.Goto(req.URL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateNetworkidle,
			Timeout:   playwright.Float(timeoutMS),
		}); err != nil {
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "failed to load target URL", "details": err.Error()})
			return
		}

		if req.WaitFor != "" {
			if _, err := page.WaitForSelector(req.WaitFor, playwright.PageWaitForSelectorOptions{
				Timeout: playwright.Float(timeoutMS),
			}); err != nil {
				c.JSON(http.StatusGatewayTimeout, gin.H{"error": "wait_for selector timeout", "details": err.Error()})
				return
			}
		}

		if req.WaitTime > 0 {
			page.WaitForTimeout(float64(req.WaitTime))
		}

		screenshotType := playwright.ScreenshotType(req.Format)

		var img []byte
		if req.Selector != "" {
			selectorScreenshotOptions := playwright.LocatorScreenshotOptions{
				Type: &screenshotType,
			}
			if req.Format == "jpeg" || req.Format == "webp" {
				selectorScreenshotOptions.Quality = playwright.Int(req.Quality)
			}
			selectorScreenshotOptions.Timeout = playwright.Float(timeoutMS)

			img, err = page.Locator(req.Selector).Screenshot(selectorScreenshotOptions)
		} else {
			screenshotOptions := playwright.PageScreenshotOptions{
				FullPage: playwright.Bool(req.FullPage),
				Type:     &screenshotType,
			}
			if req.Format == "jpeg" || req.Format == "webp" {
				screenshotOptions.Quality = playwright.Int(req.Quality)
			}
			if req.Clip != nil {
				screenshotOptions.Clip = &playwright.Rect{
					X:      req.Clip.X,
					Y:      req.Clip.Y,
					Width:  req.Clip.Width,
					Height: req.Clip.Height,
				}
			}

			img, err = page.Screenshot(screenshotOptions)
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to screenshot", "details": err.Error()})
			return
		}

		switch req.Format {
		case "jpeg":
			c.Data(http.StatusOK, "image/jpeg", img)
		case "webp":
			c.Data(http.StatusOK, "image/webp", img)
		default:
			c.Data(http.StatusOK, "image/png", img)
		}
	}
}

func main() {
	playwrightWS := os.Getenv("PLAYWRIGHT_WS_ENDPOINT")

	if err := ensurePlaywrightDriver(); err != nil {
		log.Printf("warning: playwright driver install failed at startup: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		configured := playwrightWS != ""
		status := http.StatusOK
		state := "ok"
		if !configured {
			status = http.StatusServiceUnavailable
			state = "degraded"
		}

		c.JSON(status, gin.H{
			"status":                   state,
			"time":                     time.Now().UTC().Format(time.RFC3339),
			"playwright_ws_configured": configured,
		})
	})

	r.GET("/screenshot", screenshotHandler(playwrightWS))
	r.POST("/screenshot", screenshotHandler(playwrightWS))

	if err := r.Run(":" + port); err != nil {
		log.Fatalf("server start failed: %v", err)
	}
}
