package api

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
)

func init() {
	// Optimize HTTP transport for high concurrency to cloud providers
	// Default MaxIdleConnsPerHost is 2, causing connection thrashing
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.MaxIdleConns = 1000
		t.MaxIdleConnsPerHost = 100
	}
}

// RetryTransport is an http.RoundTripper that retries on transient errors and rate limits.
type RetryTransport struct {
	Base http.RoundTripper
}

// RoundTrip executes a single HTTP transaction, returning a Response for the provided Request.
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var bodyBytes []byte
	var err error

	// Read the body if it exists so we can rewind it for retries.
	// We only buffer if GetBody is nil and ContentLength is reasonable (< 128KB).
	// Buffering large streams breaks pipelined streaming and consumes excessive memory.
	if req.Body != nil && req.GetBody == nil && req.ContentLength >= 0 && req.ContentLength < 128*1024 {
		bodyBytes, err = io.ReadAll(req.Body)
		if err == nil {
			req.Body.Close()
			req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			// Provide GetBody so it can be re-read
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewBuffer(bodyBytes)), nil
			}
		}
	}

	attempt := 0
	return WithRetryT(func() (*http.Response, error) {
		attempt++
		// If we need to retry and the body was consumed, we rewind it.
		if req.Body != nil {
			if req.GetBody != nil {
				body, _ := req.GetBody()
				req.Body = body
			} else if attempt > 1 {
				// We cannot rewind the body, so we cannot retry
				fromRetry := HTTPError{Code: 429, Status: "Cannot retry at HTTP layer: body cannot be rewound"}
				return nil, backoff.Permanent(fromRetry)
			}
		}

		resp, err := t.Base.RoundTrip(req)
		if err != nil {
			return nil, err
		}

		// Check for rate limit or server errors
		if isRetriableStatusCode(resp.StatusCode) {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			// Read body to prevent leak, then close
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, HTTPError{Code: resp.StatusCode, Status: resp.Status, RetryAfter: retryAfter}
		}

		return resp, nil
	})
}

// HTTPError represents an HTTP status error
type HTTPError struct {
	Code       int
	Status     string
	RetryAfter time.Duration
}

func (e HTTPError) Error() string {
	return e.Status
}

// Ensure HTTPError implements IsRetriableError logic
func (e HTTPError) StatusCode() int {
	return e.Code
}

// parseRetryAfter parses the Retry-After header value.
// It supports both delay-seconds and HTTP-date formats.
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// NewRetryClient returns an http.Client that retries transient errors
func NewRetryClient(baseClient *http.Client) *http.Client {
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	transport := baseClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	newClient := *baseClient
	newClient.Transport = &RetryTransport{Base: transport}
	return &newClient
}
