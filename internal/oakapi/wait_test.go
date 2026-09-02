package oakapi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Poll must keep retrying a transient error and return the successful
// value once check stops failing - the normal "stack still booting" path.
func TestPollRetriesTransientAndStopsOnTerminal(t *testing.T) {
	t.Run("succeeds after N transient errors", func(t *testing.T) {
		calls := 0
		got, err := Poll(Wait{Timeout: time.Second, Interval: time.Millisecond, Desc: "widget"}, func() (string, error) {
			calls++
			if calls < 3 {
				return "", errors.New("not ready yet")
			}
			return "widget-ready", nil
		})
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if got != "widget-ready" {
			t.Errorf("Poll = %q, want %q", got, "widget-ready")
		}
		if calls != 3 {
			t.Errorf("check called %d times, want 3", calls)
		}
	})

	// A terminal error must abort after the very first check - retrying it
	// would burn the full timeout waiting on a job that will never recover
	// (see (*Job).NotFailed).
	t.Run("terminal error stops after exactly one poll", func(t *testing.T) {
		calls := 0
		_, err := Poll(Wait{Timeout: time.Minute, Interval: time.Millisecond, Desc: "job"}, func() (string, error) {
			calls++
			return "", Terminal("job entered failure state FAILED")
		})
		if err == nil {
			t.Fatal("Poll = nil error, want the terminal error")
		}
		if !IsTerminal(err) {
			t.Errorf("Poll error = %v, want a Terminal error", err)
		}
		if calls != 1 {
			t.Errorf("check called %d times, want exactly 1", calls)
		}
	})

	// The timeout message is the only diagnostic a caller gets for a stack
	// that never came up - it must name what was being waited for and why
	// the last attempt failed, not just "timed out".
	t.Run("timeout message contains the last transient error", func(t *testing.T) {
		_, err := Poll(Wait{Timeout: 5 * time.Millisecond, Interval: time.Millisecond, Desc: "an active cluster"}, func() (string, error) {
			return "", errors.New("connection refused")
		})
		if err == nil {
			t.Fatal("Poll = nil error, want a timeout error")
		}
		if !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("Poll error = %q, want it to contain the last transient error", err.Error())
		}
		if !strings.Contains(err.Error(), "an active cluster") {
			t.Errorf("Poll error = %q, want it to contain Desc", err.Error())
		}
	})
}
