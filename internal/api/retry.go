package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/td/tgerr"
	"github.com/microsoftgraph/msgraph-sdk-go/models/odataerrors"
	"google.golang.org/api/googleapi"
)

func isRetriableStatusCode(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code < 600)
}

// IsRetriableError determines if an error is transient and should be retried.
func IsRetriableError(err error) bool {
	if err == nil {
		return false
	}

	// Do not retry context cancellation or deadline exceeded
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Check for rate limit (429) or server errors (5xx)
	// Some API clients might return errors with a StatusCode method
	type statusCodeError interface {
		StatusCode() int
	}
	var httpErr statusCodeError
	if errors.As(err, &httpErr) {
		if isRetriableStatusCode(httpErr.StatusCode()) {
			return true
		}
	}

	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		if isRetriableStatusCode(gErr.Code) {
			return true
		}
	}

	var odataErr *odataerrors.ODataError
	if errors.As(err, &odataErr) {
		if isRetriableStatusCode(odataErr.ResponseStatusCode) {
			return true
		}
	}

	// Also check typical string contained in googleapi errors (as fallback)
	googleErrors := []string{"Error 429", "Error 500", "Error 502", "Error 503", "Error 504"}
	for _, e := range googleErrors {
		if strings.Contains(err.Error(), "googleapi: "+e) {
			return true
		}
	}

	// Microsoft specific errors
	msErrors := []string{
		"TooManyRequests", "activityLimitReached",
		"503 Service Unavailable", "504 Gateway Timeout",
		"502 Bad Gateway", "500 Internal Server Error",
	}
	for _, e := range msErrors {
		if strings.Contains(err.Error(), e) {
			return true
		}
	}

	// Network errors
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) { //nolint:staticcheck
		return true
	}

	// Connection reset, EOF, etc.
	msg := err.Error()
	connErrors := []string{
		"connection reset by peer", "EOF", "unexpected EOF",
		"context deadline exceeded", "read: connection timed out",
		"client connection lost", "FLOOD_WAIT",
		"forcibly closed",  // Windows WSAECONNRESET
		"wsasend", "wsarecv", // Windows socket send/recv errors
	}
	for _, e := range connErrors {
		if strings.Contains(msg, e) {
			return true
		}
	}

	return false
}

// WithRetry executes the given operation with exponential backoff.
// It only retries on transient network errors and rate limits.
// When the error carries a Retry-After duration (from an HTTP 429 response),
// that delay is respected before the next attempt.
func WithRetry(operation func() error) error {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 500 * time.Millisecond
	b.MaxInterval = 30 * time.Second
	b.MaxElapsedTime = 5 * time.Minute

	return backoff.Retry(func() error {
		err := operation()
		if err == nil {
			return nil
		}
		if _, ok := err.(*backoff.PermanentError); ok {
			return err
		}
		if IsRetriableError(err) {
			// Respect Retry-After header if the server told us how long to wait.
			var wait time.Duration
			var httpErr HTTPError
			if errors.As(err, &httpErr) && httpErr.RetryAfter > 0 {
				wait = httpErr.RetryAfter
			} else if d, ok := tgerr.AsFloodWait(err); ok {
				wait = d
			}

			if wait > 0 {
				if wait > 2*time.Minute {
					wait = 2 * time.Minute
				}
				time.Sleep(wait)
			}
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
