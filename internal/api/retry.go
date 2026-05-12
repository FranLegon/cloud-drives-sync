package api

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// IsRetriableError determines if an error is transient and should be retried.
func IsRetriableError(err error) bool {
	if err == nil {
		return false
	}
	
	// Check for rate limit (429) or server errors (5xx)
	// Some API clients might return errors with a StatusCode method
	type statusCodeError interface {
		StatusCode() int
	}
	var httpErr statusCodeError
	if errors.As(err, &httpErr) {
		code := httpErr.StatusCode()
		if code == http.StatusTooManyRequests || (code >= 500 && code < 600) {
			return true
		}
	}
	
	// Also check typical string contained in googleapi errors (as fallback)
	if strings.Contains(err.Error(), "googleapi: Error 429") || 
		strings.Contains(err.Error(), "googleapi: Error 500") ||
		strings.Contains(err.Error(), "googleapi: Error 502") ||
		strings.Contains(err.Error(), "googleapi: Error 503") ||
		strings.Contains(err.Error(), "googleapi: Error 504") {
		return true
	}
	
	// Microsoft specific errors
	if strings.Contains(err.Error(), "TooManyRequests") || 
		strings.Contains(err.Error(), "activityLimitReached") ||
		strings.Contains(err.Error(), "503 Service Unavailable") ||
		strings.Contains(err.Error(), "504 Gateway Timeout") ||
		strings.Contains(err.Error(), "502 Bad Gateway") ||
		strings.Contains(err.Error(), "500 Internal Server Error") {
		return true
	}

	// Network errors
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) { //nolint:staticcheck
		return true
	}
	
	// Connection reset, EOF, etc.
	msg := err.Error()
	if strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "unexpected EOF") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "read: connection timed out") ||
		strings.Contains(msg, "client connection lost") ||
		strings.Contains(msg, "FLOOD_WAIT") {
		return true
	}

	return false
}

// WithRetry executes the given operation with exponential backoff.
// It only retries on transient network errors and rate limits.
func WithRetry(operation func() error) error {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 500 * time.Millisecond
	b.MaxInterval = 30 * time.Second
	b.MaxElapsedTime = 2 * time.Minute

	return backoff.Retry(func() error {
		err := operation()
		if err == nil {
			return nil
		}
		if _, ok := err.(*backoff.PermanentError); ok {
			return err
		}
		if IsRetriableError(err) {
			return err // Returning error triggers a retry
		}
		return backoff.Permanent(err) // Wrap non-retriable errors
	}, b)
}

// WithRetryT executes the given operation returning T and error with exponential backoff.
func WithRetryT[T any](operation func() (T, error)) (T, error) {
	var result T
	err := WithRetry(func() error {
		res, err := operation()
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	return result, err
}
