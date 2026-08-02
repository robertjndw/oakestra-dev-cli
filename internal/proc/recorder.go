package proc

import "strings"

// Recorder is the Runner used in tests: it starts nothing, remembers every
// Spec in order, and answers with whatever result was registered for it.
//
// It lives in the package proper rather than in a _test.go file because the
// packages under test are elsewhere (internal/compose, internal/build,
// cmd/oak-dev). It deliberately does not import testing, so no test-only flag
// registration leaks into the oak-dev binary; assertions are left to the
// caller, which matches how the existing tests in this repo compare values.
type Recorder struct {
	// Specs is every command that would have run, in order.
	Specs []Spec

	rules []rule
	// Err, when set, is returned for any Spec no rule matched. Useful for
	// proving a code path issues no commands at all beyond an expected point.
	Err error
}

type rule struct {
	match func(Spec) bool
	out   []byte
	err   error
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// On registers a canned result for every Spec match accepts. Rules are tried
// in registration order and the first match wins, so a specific rule
// registered before a general one takes precedence. Returns the Recorder so
// registrations chain.
func (r *Recorder) On(match func(Spec) bool, out []byte, err error) *Recorder {
	r.rules = append(r.rules, rule{match: match, out: out, err: err})
	return r
}

// OnContains is the common case of On: match any command whose rendered form
// contains sub. `which bash` and `config --services` are both selected this
// way, from call sites that branch on the result.
func (r *Recorder) OnContains(sub string, out []byte, err error) *Recorder {
	return r.On(func(s Spec) bool { return strings.Contains(s.String(), sub) }, out, err)
}

// Run implements Runner.
func (r *Recorder) Run(s Spec) error {
	r.Specs = append(r.Specs, s)
	_, err := r.result(s)
	return err
}

// Capture implements Runner.
func (r *Recorder) Capture(s Spec) ([]byte, error) {
	r.Specs = append(r.Specs, s)
	return r.result(s)
}

func (r *Recorder) result(s Spec) ([]byte, error) {
	for _, rl := range r.rules {
		if rl.match(s) {
			return rl.out, rl.err
		}
	}
	return nil, r.Err
}

// Commands returns every recorded command rendered as a string, in order.
func (r *Recorder) Commands() []string {
	out := make([]string, len(r.Specs))
	for i, s := range r.Specs {
		out[i] = s.String()
	}
	return out
}

// CommandsRel is Commands with root stripped back to a relative path, so a
// golden assertion does not embed the absolute location of the checkout (or
// of a t.TempDir) and stays portable between machines and runs.
func (r *Recorder) CommandsRel(root string) []string {
	out := make([]string, len(r.Specs))
	for i, s := range r.Specs {
		out[i] = s.Rel(root)
	}
	return out
}

// Reset drops the recorded commands, keeping the registered rules - for a
// test that exercises several phases and asserts on each separately.
func (r *Recorder) Reset() { r.Specs = nil }

var _ Runner = (*Recorder)(nil)
