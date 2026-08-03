package proc

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSpecString(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want string
	}{
		{"program only", Spec{Name: "docker"}, "docker"},
		{"with args", Spec{Name: "docker", Args: []string{"compose", "up", "-d"}}, "docker compose up -d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Rel is what keeps a golden assertion from embedding the absolute path of
// the checkout it happened to run in.
func TestSpecRel(t *testing.T) {
	root := "/Users/x/oakestra-macos-testing"
	s := Spec{Name: "docker", Args: []string{
		"compose", "-f", root + "/compose/worker.yml", "restart", "worker",
	}}

	if got, want := s.Rel(root), "docker compose -f compose/worker.yml restart worker"; got != want {
		t.Errorf("Rel() = %q, want %q", got, want)
	}
	// An empty root must not turn every path into a relative one by accident.
	if got := s.Rel(""); got != s.String() {
		t.Errorf("Rel(\"\") = %q, want the unmodified %q", got, s.String())
	}
}

func TestOSRunReportsExitStatus(t *testing.T) {
	if err := (OS{}).Run(Spec{Name: "sh", Args: []string{"-c", "exit 0"}}); err != nil {
		t.Errorf("Run(exit 0) = %v, want nil", err)
	}
	if err := (OS{}).Run(Spec{Name: "sh", Args: []string{"-c", "exit 3"}}); err == nil {
		t.Error("Run(exit 3) = nil, want an error")
	}
}

// Combined is not cosmetic: `go env GOPATH` is parsed as a path and must not
// have stderr mixed into it, while a failed `docker compose exec` is reported
// to the user with its stderr attached.
func TestOSCaptureCombined(t *testing.T) {
	spec := Spec{Name: "sh", Args: []string{"-c", "echo out; echo err >&2"}}

	out, err := (OS{}).Capture(spec)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "out" {
		t.Errorf("Capture() = %q, want just %q", got, "out")
	}

	spec.Combined = true
	out, err = (OS{}).Capture(spec)
	if err != nil {
		t.Fatalf("Capture(combined): %v", err)
	}
	for _, want := range []string{"out", "err"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("Capture(combined) = %q, want it to contain %q", out, want)
		}
	}
}

// A nil Env means os/exec's own "inherit", not "run with nothing set" - the
// difference decides whether `go build` can find the module cache.
func TestOSCaptureEnvNilInherits(t *testing.T) {
	t.Setenv("OAK_DEV_PROC_TEST", "inherited")
	spec := Spec{Name: "sh", Args: []string{"-c", "echo $OAK_DEV_PROC_TEST"}}

	out, err := (OS{}).Capture(spec)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "inherited" {
		t.Errorf("with Env nil, got %q, want %q", got, "inherited")
	}

	spec.Env = []string{}
	out, err = (OS{}).Capture(spec)
	if err != nil {
		t.Fatalf("Capture(empty env): %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "" {
		t.Errorf("with Env empty, got %q, want it unset", got)
	}
}

func TestOSCaptureRunsInDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := (OS{}).Capture(Spec{Name: "ls", Dir: dir})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "marker" {
		t.Errorf("ls in Dir = %q, want %q", got, "marker")
	}
}

func TestRecorderRecordsInOrder(t *testing.T) {
	rec := NewRecorder()
	_ = rec.Run(Spec{Name: "docker", Args: []string{"compose", "restart", "worker"}})
	_, _ = rec.Capture(Spec{Name: "docker", Args: []string{"compose", "config", "--services"}})

	want := []string{
		"docker compose restart worker",
		"docker compose config --services",
	}
	if got := rec.Commands(); !slices.Equal(got, want) {
		t.Errorf("Commands() = %v, want %v", got, want)
	}
}

// Both Run and Capture record, so a sequence mixing them reads as one list.
func TestRecorderFirstMatchingRuleWins(t *testing.T) {
	boom := errors.New("boom")
	rec := NewRecorder().
		OnContains("which bash", []byte("/bin/bash"), nil).
		OnContains("which", nil, boom)

	out, err := rec.Capture(Spec{Name: "docker", Args: []string{"exec", "worker", "which", "bash"}})
	if err != nil {
		t.Errorf("err = %v, want nil from the first matching rule", err)
	}
	if got := string(out); got != "/bin/bash" {
		t.Errorf("out = %q, want %q", got, "/bin/bash")
	}

	// The later, more general rule still applies to anything the first missed.
	if _, err := rec.Capture(Spec{Name: "docker", Args: []string{"exec", "worker", "which", "sh"}}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestRecorderDefaultErr(t *testing.T) {
	boom := errors.New("boom")
	rec := NewRecorder()
	rec.Err = boom

	if err := rec.Run(Spec{Name: "docker"}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
	// It still records, so a test can assert what was attempted before failing.
	if got, want := rec.Commands(), []string{"docker"}; !slices.Equal(got, want) {
		t.Errorf("Commands() = %v, want %v", got, want)
	}
}

func TestRecorderCommandsRel(t *testing.T) {
	root := t.TempDir()
	rec := NewRecorder()
	_ = rec.Run(Spec{Name: "docker", Args: []string{
		"compose", "-f", filepath.Join(root, "compose", "worker.yml"), "up", "-d",
	}})

	want := []string{"docker compose -f compose/worker.yml up -d"}
	if got := rec.CommandsRel(root); !slices.Equal(got, want) {
		t.Errorf("CommandsRel() = %v, want %v", got, want)
	}
}

func TestRecorderResetKeepsRules(t *testing.T) {
	rec := NewRecorder().OnContains("which bash", []byte("/bin/bash"), nil)
	_ = rec.Run(Spec{Name: "docker"})
	rec.Reset()

	if got := rec.Commands(); len(got) != 0 {
		t.Errorf("Commands() after Reset = %v, want none", got)
	}
	out, _ := rec.Capture(Spec{Name: "docker", Args: []string{"exec", "worker", "which", "bash"}})
	if got := string(out); got != "/bin/bash" {
		t.Errorf("rule lost after Reset: out = %q", got)
	}
}
