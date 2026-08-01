// Package multilog runs several long-lived commands concurrently and
// multiplexes their stdout/stderr into one stream, each line prefixed with a
// short tag, so `oak-dev logs` and `oak-dev dev` can show "one terminal"
// instead of N separate `docker compose logs` windows.
package multilog

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

var colors = []string{"\033[36m", "\033[35m", "\033[33m", "\033[32m", "\033[34m", "\033[31m"}

const reset = "\033[0m"

// Source is one command to stream, tagged with a short prefix.
type Source struct {
	Tag  string
	Dir  string
	Env  []string
	Name string
	Args []string
}

// Run starts every source concurrently and blocks until ctx is cancelled or
// every command exits. Each line is written to stdout as "[tag] line" (with
// a distinct color per tag when stdout is a terminal-friendly fd).
func Run(ctx context.Context, sources []Source) error {
	var wg sync.WaitGroup
	var mu sync.Mutex

	width := 0
	for _, s := range sources {
		if len(s.Tag) > width {
			width = len(s.Tag)
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
	case <-ctx.Done():
		return nil
	case <-done:
		return nil
	}
}

func runOne(ctx context.Context, s Source, color string, width int, mu *sync.Mutex) {
	cmd := exec.CommandContext(ctx, s.Name, s.Args...)
	cmd.Dir = s.Dir
	if s.Env != nil {
		cmd.Env = s.Env
	}
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

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		printLine(mu, s.Tag, color, width, scanner.Text())
	}
	_ = scanner.Err() // a truncated final line on process exit is not an error worth surfacing

	_ = cmd.Wait()
}

func printLine(mu *sync.Mutex, tag, color string, width int, line string) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(os.Stdout, "%s%-*s%s | %s\n", color, width, tag, reset, line)
}
