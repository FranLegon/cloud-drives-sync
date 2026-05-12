package api

import (
	"bytes"
	"io"
	"net/http"

	"github.com/cenkalti/backoff/v4"
)

// RetryTransport is an http.RoundTripper that retries on transient errors and rate limits.
type RetryTransport struct {
	Base http.RoundTripper
}

// RoundTrip executes a single HTTP transaction, returning a Response for the provided Request.
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var bodyBytes []byte
	var err error
	
	// Read the body if it exists so we can rewind it for retries.
	// We only buffer if GetBody is nil and ContentLength is reasonable (< 32MB).
	if req.Body != nil && req.GetBody == nil && req.ContentLength >= 0 && req.ContentLength < 32*1024*1024 {
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
				fromRetry := HTTPError{Code: 400, Status: "Cannot retry: body cannot be rewound"}
				return nil, backoff.Permanent(fromRetry)
			}
		}

		resp, err := t.Base.RoundTrip(req)
		if err != nil {
			return nil, err
		}

		// Check for rate limit or server errors
		if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode < 600) {
			// Read body to prevent leak, then close
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, HTTPError{Code: resp.StatusCode, Status: resp.Status}
		}

		return resp, nil
	})
}

// HTTPError represents an HTTP status error
type HTTPError struct {
	Code   int
	Status string
}

func (e HTTPError) Error() string {
	return e.Status
}

// Ensure HTTPError implements IsRetriableError logic
func (e HTTPError) StatusCode() int {
	return e.Code
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
