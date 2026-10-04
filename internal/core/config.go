package core

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// MinTraceraVersion is the lowest server version this adapter talks to.
const MinTraceraVersion = "0.4.0"

const (
	uploadJobConcurrency = 4
	uploadConcurrencyMax = 16
	flushMaxItemsCap     = 50
)

// Config is the resolved TRACERA_* configuration for one test process.
type Config struct {
	BaseURL           string
	Token             string
	ProjectID         int
	TestRunID         int // 0 when not set
	RunTitle          string
	RequireReport     bool
	FlushMaxItems     int
	FlushMaxMS        int
	UploadConcurrency int
}

// ParseBool reads the TRACERA_* boolean spelling (true/1, false/0).
func ParseBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		return fallback
	}
}

var strictIntRe = regexp.MustCompile(`^[+-]?\d+$`)

func parseStrictInt(raw string) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strictIntRe.MatchString(trimmed) {
		return 0, false
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parsePositiveInt(raw string, fallback int) int {
	n, ok := parseStrictInt(raw)
	if !ok || n <= 0 {
		return fallback
	}
	return n
}

func parseUploadConcurrency(raw string) int {
	n := parsePositiveInt(raw, uploadJobConcurrency)
	if n > uploadConcurrencyMax {
		return uploadConcurrencyMax
	}
	return n
}

// LoadConfig parses TRACERA_* keys from an already-resolved env map
// (process first, then nearest .env filling missing/empty TRACERA_* only).
func LoadConfig(env map[string]string) (*Config, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(env["TRACERA_BASE_URL"]), "/")
	token := strings.TrimSpace(env["TRACERA_AUTOTEST_TOKEN"])
	if baseURL == "" {
		return nil, errors.New("TRACERA_BASE_URL is required")
	}
	if token == "" {
		return nil, errors.New("TRACERA_AUTOTEST_TOKEN is required")
	}
	projectID, ok := parseStrictInt(env["TRACERA_PROJECT_ID"])
	if !ok || projectID < 1 {
		return nil, errors.New("TRACERA_PROJECT_ID must be a positive integer")
	}

	testRunID := 0
	if raw := strings.TrimSpace(env["TRACERA_TEST_RUN_ID"]); raw != "" {
		var okRun bool
		testRunID, okRun = parseStrictInt(raw)
		if !okRun || testRunID < 1 {
			return nil, errors.New("TRACERA_TEST_RUN_ID must be a positive integer when set")
		}
	}

	runTitle := strings.TrimSpace(env["TRACERA_RUN_TITLE"])
	if len([]rune(runTitle)) > 128 {
		return nil, errors.New("TRACERA_RUN_TITLE must be at most 128 characters")
	}

	flushMaxItems := parsePositiveInt(env["TRACERA_FLUSH_MAX_ITEMS"], flushMaxItemsCap)
	if flushMaxItems > flushMaxItemsCap {
		flushMaxItems = flushMaxItemsCap
	}

	return &Config{
		BaseURL:           baseURL,
		Token:             token,
		ProjectID:         projectID,
		TestRunID:         testRunID,
		RunTitle:          runTitle,
		RequireReport:     ParseBool(env["TRACERA_REQUIRE_REPORT"], false),
		FlushMaxItems:     flushMaxItems,
		FlushMaxMS:        parsePositiveInt(env["TRACERA_FLUSH_MAX_MS"], 1000),
		UploadConcurrency: parseUploadConcurrency(env["TRACERA_UPLOAD_CONCURRENCY"]),
	}, nil
}
