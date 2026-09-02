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
// Unlike Python's wait_until, check has no truthy/falsy path: a "not ready
// yet" result must come back as a non-nil, non-terminal error, never as a
// zero value with a nil error. wait_until's `return None` case was actively
// harmful - it reset last_error to nil, so a poll that returned falsy on
// every attempt timed out with no explanation at all. Requiring a
// descriptive error on every non-success return means every timeout here
// carries the last observed reason.
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
		// The deadline is checked before every check call except the very
		// first, so Poll always tries at least once even with a tiny or zero
		// Timeout, but never makes one extra call after the budget is spent -
		// a long default Interval (3s) must not buy check() a bonus attempt
		// once the deadline has already passed during that sleep.
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
