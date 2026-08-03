// Package multilog runs several long-lived commands concurrently and
// multiplexes their stdout/stderr into one stream, each line prefixed with a
// short tag, so `oak-dev logs` and `oak-dev dev` can show "one terminal"
// instead of N separate `docker compose logs` windows.
package multilog

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

var colors = []string{"\033[36m", "\033[35m", "\033[33m", "\033[32m", "\033[34m", "\033[31m"}

const reset = "\033[0m"

// shutdownGrace bounds how long Run waits, once ctx is cancelled, for
// sources to finish on their own before returning anyway. Command-backed
// sources are asked to exit via SIGTERM (see runOne); func-backed sources
// (the file watchers) are expected to honor ctx themselves. A source still
// running past this deadline is left running rather than making `oak-dev
// dev` itself unkillable - see cmd_dev.go's watchSources for why an
// in-flight reload uses context.WithoutCancel to survive it.
const shutdownGrace = 10 * time.Second

// Source is one thing to stream, tagged with a short prefix. Exactly one of
// Name (a command to run) or Func (in-process work) must be set.
type Source struct {
	Tag string

	// Dir and Env apply only to the Name/Args command form (cmd.Dir = Dir,
	// cmd.Env = Env); a Func-backed source manages its own working
	// directory and environment internally and ignores both.
	Dir string
	Env []string

	// Name/Args describe an external command, run with cmd.Dir = Dir and
	// cmd.Env = Env.
	Name string
	Args []string

	// Func, when set, replaces Name/Args: it runs in-process rather than as
	// an external command. Lines written to out are tagged and interleaved
	// exactly like a command's stdout. Used by the source-change watchers,
	// which used to shell out to the external `watchexec` binary.
	Func func(ctx context.Context, out io.Writer) error
}

// Run starts every source concurrently and blocks until ctx is cancelled or
// every source exits. Each line is written to stdout as "[tag] line" (with
// a distinct color per tag when stdout is a terminal-friendly fd).
//
// On cancellation, if any source is Func-backed, Run gives every source up
// to shutdownGrace to exit on its own - long enough for an in-flight reload
// to finish - before returning regardless. Command-only source sets (e.g.
// `oak-dev logs`, which has no reload to protect) return as soon as ctx is
// cancelled, same as before this grace period existed; there's nothing
// there that benefits from the wait, only a command that would otherwise
// hang for up to shutdownGrace on a slow `docker compose logs -f` exit.
func Run(ctx context.Context, sources []Source) error {
	var wg sync.WaitGroup
	var mu sync.Mutex

	width := 0
	hasFunc := false
	for _, s := range sources {
		if len(s.Tag) > width {
			width = len(s.Tag)
		}
		if s.Func != nil {
			hasFunc = true
		}
	}

	for i, s := range sources {
		color := colors[i%len(colors)]
		wg.Add(1)
		go func(s Source, color string) {
			defer wg.Done()
			runOne(ctx, s, color, width, &mu)
		}(s, color)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}

	if !hasFunc {
		return nil
	}

	select {
	case <-done:
	case <-time.After(shutdownGrace):
	}
	return nil
}

func runOne(ctx context.Context, s Source, color string, width int, mu *sync.Mutex) {
	if s.Func != nil {
		runFunc(ctx, s, color, width, mu)
		return
	}

	cmd := exec.CommandContext(ctx, s.Name, s.Args...)
	cmd.Dir = s.Dir
	if s.Env != nil {
		cmd.Env = s.Env
	}
	// A plain SIGKILL on cancel (exec.CommandContext's default) doesn't give
	// e.g. `docker compose logs -f` a chance to exit cleanly. Ask nicely
	// first; WaitDelay bounds how long we wait before killing it anyway.
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		printLine(mu, s.Tag, color, width, fmt.Sprintf("oak-dev: %v", err))
		return
	}
	cmd.Stderr = cmd.Stdout // interleave; compose already tags its own stream names

	if err := cmd.Start(); err != nil {
		printLine(mu, s.Tag, color, width, fmt.Sprintf("oak-dev: %v", err))
		return
	}

	scanLines(stdout, s.Tag, color, width, mu)
	_ = cmd.Wait()
}

// runFunc runs a func-backed source, piping anything it writes through the
// same tagged/colored line scanner a command's stdout goes through.
func runFunc(ctx context.Context, s Source, color string, width int, mu *sync.Mutex) {
	pr, pw := io.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		err := s.Func(ctx, pw)
		_ = pw.CloseWithError(err)
	}()

	scanLines(pr, s.Tag, color, width, mu)
	<-done
}

func scanLines(r io.Reader, tag, color string, width int, mu *sync.Mutex) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		printLine(mu, tag, color, width, scanner.Text())
	}
	_ = scanner.Err() // a truncated final line on process exit is not an error worth surfacing
}

func printLine(mu *sync.Mutex, tag, color string, width int, line string) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(os.Stdout, "%s%-*s%s | %s\n", color, width, tag, reset, line)
}
