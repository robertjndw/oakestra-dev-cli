package oakapi

import (
	"errors"
	"fmt"
	"time"
)

// terminalError marks a failure that must abort polling immediately rather
// than being retried. It is the Go equivalent of helpers.py's AssertionError
// contract: wait_until there re-raises AssertionError but swallows every
// other exception until its deadline; Poll re-raises a terminalError the
// same way and treats every other error as transient.
type terminalError struct {
	err error
}

func (t *terminalError) Error() string { return t.err.Error() }
func (t *terminalError) Unwrap() error { return t.err }

// Terminal builds an error that Poll will not retry - use it from inside a
// check function to fail fast (see (*Job).NotFailed).
func Terminal(format string, a ...any) error {
	return &terminalError{err: fmt.Errorf(format, a...)}
}

// IsTerminal reports whether err (or something it wraps) was built by Terminal.
func IsTerminal(err error) bool {
	var t *terminalError
	return errors.As(err, &t)
}

// Wait configures a Poll call. A zero Interval defaults to 3s, matching
// helpers.py's wait_until default.
type Wait struct {
	Timeout  time.Duration
	Interval time.Duration
	Desc     string // what's being waited for, used in the timeout message
}

// Poll calls check repeatedly until it succeeds, returns a Terminal error, or
// w.Timeout elapses.
//
// Unlike Python's wait_until, check has no truthy/falsy path: "not ready
// yet" must be a real error, never a zero value with nil error. wait_until's
// `return None` case actually made debugging harder in the old suite - it
// reset last_error to nil, so a poll that kept returning falsy timed out
// with no explanation. Requiring an error on every non-success return means
// a timeout here always carries the last observed reason.
func Poll[T any](w Wait, check func() (T, error)) (T, error) {
	interval := w.Interval
	if interval <= 0 {
		interval = 3 * time.Second
	}

	var zero T
	deadline := time.Now().Add(w.Timeout)
	var lastErr error
	timeout := func() (T, error) {
		return zero, fmt.Errorf("timed out after %s waiting for %s: %w", w.Timeout, w.Desc, lastErr)
	}

	for first := true; ; first = false {
		// Skip the deadline check on the first pass so Poll always tries at
		// least once, even with a tiny or zero Timeout. After that, check
		// again before every call: a 3s default Interval shouldn't buy
		// check() a bonus attempt just because the deadline passed during
		// the last sleep.
		if !first && !time.Now().Before(deadline) {
			return timeout()
		}

		v, err := check()
		if err == nil {
			return v, nil
		}
		if IsTerminal(err) {
			return zero, err
		}
		lastErr = err

		if !time.Now().Before(deadline) {
			return timeout()
		}
		time.Sleep(interval)
	}
}
