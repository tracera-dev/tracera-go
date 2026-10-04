package core

import (
	"net/http"
	"time"
)

// Request timeouts shared by every language adapter.
const (
	HTTPTimeout       = 10 * time.Second
	HTTPUploadTimeout = 300 * time.Second
)

var (
	apiClient = &http.Client{
		Timeout:   HTTPTimeout,
		Transport: sharedTransport(),
	}
	uploadClient = &http.Client{
		Timeout:   HTTPUploadTimeout,
		Transport: sharedTransport(),
	}
)

func sharedTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 16
	t.MaxConnsPerHost = 16
	return t
}
