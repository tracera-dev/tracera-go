package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Blob upload policy (shared across languages).
const (
	UploadMaxAttempts = 3
	uploadRetryBaseMS = 200
)

// AppCapabilities mirrors the server capability flags the adapter needs.
type AppCapabilities struct {
	AutotestCreateRun    bool `json:"autotestCreateRun"`
	AutotestFinishRun    bool `json:"autotestFinishRun"`
	AutotestResultsBatch bool `json:"autotestResultsBatch"`
	TestResultEnvVars    bool `json:"testResultEnvVars"`
	AutotestBlobsBatch   bool `json:"autotestBlobsBatch"`
}

// AppConfig is the subset of /app-config the adapter reads.
type AppConfig struct {
	PublicAppURL string           `json:"publicAppUrl"`
	Version      string           `json:"version"`
	Capabilities *AppCapabilities `json:"capabilities"`
}

// BatchItem is one TestResult row on the results batch wire.
type BatchItem struct {
	IsAutomated     bool              `json:"isAutomated"`
	TestCaseID      int               `json:"testCaseId"`
	TestRunID       int               `json:"testRunId"`
	Result          string            `json:"result,omitempty"`
	AutotestName    string            `json:"autotestName,omitempty"`
	DurationMinutes *float64          `json:"durationMinutes,omitempty"`
	Setup           []StepJSON        `json:"setup"`
	Test            []StepJSON        `json:"test"`
	Teardown        []StepJSON        `json:"teardown"`
	Attachments     []map[string]any  `json:"attachments"`
	ErrorMessage    *string           `json:"errorMessage,omitempty"`
	Comment         *string           `json:"comment,omitempty"`
	EnvVars         map[string]string `json:"envVars,omitempty"`
}

// BatchItemResult is the per-row server verdict.
type BatchItemResult struct {
	Index int    `json:"index"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// BatchResponse is the results batch reply.
type BatchResponse struct {
	TestRunID int               `json:"testRunId"`
	Results   []BatchItemResult `json:"results"`
}

// BlobFile is an attachment body: either in-memory Data or a disk Path.
// Path blobs stay path-only and stream on upload (no base64 in memory).
type BlobFile struct {
	FileName string
	MimeType string
	Data     []byte
	Path     string
}

// BlobUploadResult identifies an uploaded blob.
type BlobUploadResult struct {
	ID        string `json:"id"`
	FileName  string `json:"fileName"`
	MimeType  string `json:"mimeType"`
	SizeBytes int64  `json:"sizeBytes"`
}

// Client talks to the Tracera Autotest API.
type Client struct {
	config    *Config
	mu        sync.Mutex
	appConfig *AppConfig
}

// NewClient builds an API client for the resolved config.
func NewClient(config *Config) *Client { return &Client{config: config} }

func (c *Client) headers(req *http.Request, json bool) {
	req.Header.Set("Authorization", "Bearer "+c.config.Token)
	if json {
		req.Header.Set("Content-Type", "application/json")
	}
}

func readBody(res *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8192))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// GetAppConfig fetches and caches /app-config.
func (c *Client) GetAppConfig() (*AppConfig, error) {
	req, err := http.NewRequest(http.MethodGet, c.config.BaseURL+"/app-config", nil)
	if err != nil {
		return nil, err
	}
	res, err := apiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET /app-config failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("GET /app-config failed: HTTP %d", res.StatusCode)
	}
	var parsed AppConfig
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("GET /app-config failed: %w", err)
	}
	if parsed.PublicAppURL == "" {
		parsed.PublicAppURL = c.config.BaseURL
	}
	c.mu.Lock()
	c.appConfig = &parsed
	c.mu.Unlock()
	return &parsed, nil
}

var semverRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

func parseSemver(v string) ([3]int, bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func isVersionAtLeast(actual, minimum string) bool {
	a, okA := parseSemver(actual)
	b, okB := parseSemver(minimum)
	if !okA || !okB {
		return false
	}
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

// EnsureCompatible refuses servers below MinTraceraVersion or missing the
// Autotest capabilities the adapter depends on.
func (c *Client) EnsureCompatible(app *AppConfig) error {
	if app == nil || !isVersionAtLeast(app.Version, MinTraceraVersion) {
		version := ""
		if app != nil {
			version = app.Version
		}
		return fmt.Errorf("server version %s is below the adapter's minimum %s", version, MinTraceraVersion)
	}
	caps := app.Capabilities
	if caps != nil && (!caps.AutotestCreateRun || !caps.AutotestFinishRun || !caps.AutotestResultsBatch || !caps.TestResultEnvVars) {
		return fmt.Errorf("server missing required API capabilities (create/finish run, results batch, or result envVars)")
	}
	return nil
}

// CreateRun opens an owned test run and returns its id.
func (c *Client) CreateRun() (int, error) {
	url := fmt.Sprintf("%s/projects/%d/test-runs/autotest", c.config.BaseURL, c.config.ProjectID)
	title := strings.TrimSpace(c.config.RunTitle)
	var body io.Reader
	if title != "" {
		raw, err := json.Marshal(map[string]string{"title": title})
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return 0, err
	}
	c.headers(req, title != "")
	res, err := apiClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("create run failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return 0, fmt.Errorf("create run failed: HTTP %d %s", res.StatusCode, readBody(res))
	}
	var parsed struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return 0, fmt.Errorf("create run failed: %w", err)
	}
	return parsed.ID, nil
}

// FinishRun closes an owned (or last-peer) test run.
func (c *Client) FinishRun(testRunID int) error {
	url := fmt.Sprintf("%s/projects/%d/test-runs/%d/autotest/finish", c.config.BaseURL, c.config.ProjectID, testRunID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(nil))
	if err != nil {
		return err
	}
	c.headers(req, true)
	res, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("finish run failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("finish run failed: HTTP %d %s", res.StatusCode, readBody(res))
	}
	return nil
}

var (
	retryHTTP5xx   = regexp.MustCompile(`HTTP 5\d\d`)
	retryHTTP429   = regexp.MustCompile(`HTTP 429`)
	retryTransient = regexp.MustCompile(`(?i)connection reset|timeout|timed out|EOF|network|broken pipe|aborted`)
	retryHTTP4xx   = regexp.MustCompile(`HTTP 4\d\d`)
)

func isRetryableUploadError(err error) bool {
	// A file that vanished or cannot be read will not come back on a retry.
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return false
	}
	msg := err.Error()
	switch {
	case retryHTTP5xx.MatchString(msg):
		return true
	case retryHTTP429.MatchString(msg):
		return true
	case retryTransient.MatchString(msg):
		return true
	case retryHTTP4xx.MatchString(msg):
		return false
	default:
		return true
	}
}

func withUploadRetry[T any](label string, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= UploadMaxAttempts; attempt++ {
		out, err := fn()
		if err == nil {
			return out, nil
		}
		lastErr = err
		if attempt >= UploadMaxAttempts || !isRetryableUploadError(err) {
			return zero, err
		}
		delay := time.Duration(uploadRetryBaseMS*(1<<(attempt-1))) * time.Millisecond
		LogDebug("Tracera: %s failed (%s) — retry %d/%d in %s", label, err, attempt, UploadMaxAttempts-1, delay)
		time.Sleep(delay)
	}
	return zero, lastErr
}

// PostResultsBatch uploads a batch of TestResult rows.
func (c *Client) PostResultsBatch(testRunID int, items []BatchItem) (*BatchResponse, error) {
	return withUploadRetry(fmt.Sprintf("results batch upload (%d)", len(items)), func() (*BatchResponse, error) {
		raw, err := json.Marshal(map[string]any{"testRunId": testRunID, "items": items})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, c.config.BaseURL+"/test-results/batch", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		c.headers(req, true)
		res, err := apiClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("results batch upload failed: %w", err)
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			return nil, fmt.Errorf("results batch upload failed: HTTP %d %s", res.StatusCode, readBody(res))
		}
		var parsed BatchResponse
		if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
			return nil, fmt.Errorf("results batch upload failed: %w", err)
		}
		if parsed.TestRunID == 0 {
			parsed.TestRunID = testRunID
		}
		return &parsed, nil
	})
}

func resolveMIME(file BlobFile) string {
	if file.MimeType != "" {
		return file.MimeType
	}
	return "application/octet-stream"
}

// multipartBody is one blob framed as multipart/form-data: the form header,
// the blob body, the closing boundary. Disk blobs are read from an open file
// handle as the request is written, so a large artifact never sits in memory;
// the length is known up front, so the request is not chunked.
type multipartBody struct {
	io.Reader
	closer      io.Closer
	contentType string
	length      int64
}

func (b *multipartBody) Close() error {
	if b.closer == nil {
		return nil
	}
	return b.closer.Close()
}

func openMultipart(file BlobFile) (*multipartBody, error) {
	var head bytes.Buffer
	writer := multipart.NewWriter(&head)
	if _, err := writer.CreateFormFile("file", file.FileName); err != nil {
		return nil, err
	}
	tail := []byte("\r\n--" + writer.Boundary() + "--\r\n")

	body := &multipartBody{contentType: writer.FormDataContentType()}
	var blob io.Reader
	var blobLen int64
	if file.Path != "" {
		fh, err := os.Open(file.Path)
		if err != nil {
			return nil, err
		}
		info, err := fh.Stat()
		if err != nil {
			_ = fh.Close()
			return nil, err
		}
		blobLen = info.Size()
		// Bound the read to the length announced in the header: a file that
		// grows mid-upload must not overrun Content-Length.
		blob = io.LimitReader(fh, blobLen)
		body.closer = fh
	} else {
		blob = bytes.NewReader(file.Data)
		blobLen = int64(len(file.Data))
	}
	body.length = int64(head.Len()) + blobLen + int64(len(tail))
	body.Reader = io.MultiReader(&head, blob, bytes.NewReader(tail))
	return body, nil
}

// UploadBlob uploads a single attachment with retries.
func (c *Client) UploadBlob(file BlobFile) (*BlobUploadResult, error) {
	url := fmt.Sprintf("%s/projects/%d/blobs", c.config.BaseURL, c.config.ProjectID)
	return withUploadRetry(fmt.Sprintf("attachment upload (%s)", file.FileName), func() (*BlobUploadResult, error) {
		body, err := openMultipart(file)
		if err != nil {
			return nil, fmt.Errorf("attachment upload failed: %w", err)
		}
		req, err := http.NewRequest(http.MethodPost, url, body)
		if err != nil {
			_ = body.Close()
			return nil, err
		}
		// NewRequest only knows the length of in-memory readers.
		req.ContentLength = body.length
		req.Header.Set("Authorization", "Bearer "+c.config.Token)
		req.Header.Set("Content-Type", body.contentType)
		res, err := uploadClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("attachment upload failed: %w", err)
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			return nil, fmt.Errorf("attachment upload failed: HTTP %d %s", res.StatusCode, readBody(res))
		}
		var parsed BlobUploadResult
		if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
			return nil, fmt.Errorf("attachment upload failed: %w", err)
		}
		if parsed.FileName == "" {
			parsed.FileName = file.FileName
		}
		if parsed.MimeType == "" {
			parsed.MimeType = resolveMIME(file)
		}
		return &parsed, nil
	})
}

// UploadBlobs uploads attachments with bounded concurrency.
func (c *Client) UploadBlobs(files []BlobFile) ([]*BlobUploadResult, error) {
	if len(files) == 0 {
		return nil, nil
	}
	out := make([]*BlobUploadResult, len(files))
	errs := make([]error, len(files))
	sem := make(chan struct{}, max(1, c.config.UploadConcurrency))
	var wg sync.WaitGroup
	for i, file := range files {
		wg.Add(1)
		go func(i int, file BlobFile) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("attachment upload panic: %v", r)
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = c.UploadBlob(file)
		}(i, file)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// RunURL is the customer-facing link to a test run.
func (c *Client) RunURL(testRunID int) string {
	base := c.config.BaseURL
	c.mu.Lock()
	if c.appConfig != nil && c.appConfig.PublicAppURL != "" {
		base = c.appConfig.PublicAppURL
	}
	c.mu.Unlock()
	return fmt.Sprintf("%s/project/%d/test-runs/%d", strings.TrimRight(base, "/"), c.config.ProjectID, testRunID)
}
