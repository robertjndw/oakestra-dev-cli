// Package watch reports source changes under a directory tree, replacing the
// external `watchexec` binary that `oak-dev dev` used to shell out to.
package watch

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Options configures Run.
type Options struct {
	// Dir is the tree root, watched recursively.
	Dir string
	// Exts restricts events to files with one of these extensions
	// (case-sensitive, dot included, e.g. ".go"). Defaults to []string{".go"}.
	Exts []string
	// Debounce coalesces a burst of relevant events into a single onChange
	// call, firing once this much time has passed without a new one.
	// Defaults to 300ms.
	Debounce time.Duration
	// Log receives non-fatal diagnostics (e.g. a directory that could not be
	// watched). Optional; nil discards them.
	Log func(string)
}

func (o Options) exts() []string {
	if len(o.Exts) > 0 {
		return o.Exts
	}
	return []string{".go"}
}

func (o Options) debounce() time.Duration {
	if o.Debounce > 0 {
		return o.Debounce
	}
	return 300 * time.Millisecond
}

func (o Options) log(msg string) {
	if o.Log != nil {
		o.Log(msg)
	}
}

// Run watches o.Dir recursively and calls onChange once per debounced burst
// of relevant events. It blocks until ctx is cancelled, at which point it
// returns nil.
//
// Unlike watchexec's default do-nothing busy behaviour, events that arrive
// while onChange is still running are not dropped: they coalesce into
// exactly one more call once the current one returns and the debounce
// window has again passed quietly. Saving mid-rebuild should never silently
// leave stale code running.
func Run(ctx context.Context, o Options, onChange func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()

	if err := addTree(w, o.Dir, o); err != nil {
		return err
	}

	// A dedicated pump keeps fsnotify's own channels draining at all times,
	// so a slow or long-running onChange (a cross-compile plus a container
	// restart) never backs up and stalls delivery of new events.
	type event struct {
		relevant bool
		isDir    bool
		name     string
		created  bool
	}
	pump := make(chan event, 64)
	go func() {
		defer close(pump)
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				// isDir only matters for a Create (to decide whether to
				// addTree it below), so it's the only case worth a stat.
				created := ev.Has(fsnotify.Create)
				var isDir bool
				if created {
					info, statErr := os.Stat(ev.Name)
					isDir = statErr == nil && info.IsDir()
				}
				select {
				case pump <- event{
					relevant: relevantFile(ev.Name, o.exts()),
					isDir:    isDir,
					name:     ev.Name,
					created:  created,
				}:
				case <-ctx.Done():
					return
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				o.log("watch: " + err.Error())
			case <-ctx.Done():
				return
			}
		}
	}()

	// debounce is created lazily on the first relevant event and reused
	// (Stop+Reset) for every one after that, rather than calling time.After
	// per event: a burst of N relevant events (e.g. a branch switch touching
	// many files within the debounce window) would otherwise leave N-1
	// abandoned timers to fire and be GC'd for nothing, only the last one
	// ever read.
	var debounce *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-pump:
			if !ok {
				return nil
			}
			if ev.isDir && ev.created {
				// A directory materialized (mkdir -p, git checkout of a new
				// package): watch it too, recursively, so files created
				// inside it are seen without a restart.
				if err := addTree(w, ev.name, o); err != nil {
					o.log("watch: " + err.Error())
				}
				continue
			}
			// ev.isDir is only ever true alongside ev.created, handled above.
			if !ev.relevant {
				continue
			}
			if debounce == nil {
				debounce = time.NewTimer(o.debounce())
			} else {
				if !debounce.Stop() {
					select {
					case <-debounce.C:
					default:
					}
				}
				debounce.Reset(o.debounce())
			}
			timerC = debounce.C

		case <-timerC:
			timerC = nil
			onChange()
		}
	}
}

// addTree adds a watch for dir and every subdirectory beneath it, skipping
// VCS/vendor/hidden directories. Failing to watch dir itself (e.g. it doesn't
// exist) is returned as an error; failing to watch a deeper subdirectory
// (e.g. a permission error) is only logged, so one bad subtree doesn't stop
// the rest of the walk from being watched.
func addTree(w *fsnotify.Watcher, dir string, o Options) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			o.log("watch: " + err.Error())
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if path != dir && (base == ".git" || base == "vendor" || strings.HasPrefix(base, ".")) {
			return filepath.SkipDir
		}
		if err := w.Add(path); err != nil {
			if path == dir {
				return err
			}
			o.log("watch: " + err.Error())
		}
		return nil
	})
}

// relevantFile reports whether name has one of exts and isn't an editor
// swap/backup file (leading dot or trailing tilde).
func relevantFile(name string, exts []string) bool {
	base := filepath.Base(name)
	if strings.HasPrefix(base, ".") || strings.HasSuffix(base, "~") {
		return false
	}
	return slices.Contains(exts, filepath.Ext(base))
}
