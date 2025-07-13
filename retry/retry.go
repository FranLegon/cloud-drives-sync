package retry

import (
	"fmt"
	"math/rand"
	"time"
)

// Retry retries the given function up to maxAttempts with exponential backoff.
// It logs each retry and backoff to stdout.
func Retry(maxAttempts int, baseDelay time.Duration, fn func() error) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if attempt < maxAttempts {
			delay := baseDelay * (1 << (attempt - 1))
			delay = delay + time.Duration(rand.Int63n(int64(delay/2))) // jitter
			fmt.Printf("[RETRY] Attempt %d failed: %v. Retrying in %v...\n", attempt, err, delay)
			time.Sleep(delay)
		}
	}
	return err
}
