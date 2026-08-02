package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// supportedInstallShells are the shells `completion install` knows how to
// place a script for. powershell is deliberately excluded - this is a macOS
// tool, and cobra's own `completion powershell --help` already covers it.
var supportedInstallShells = []string{"bash", "zsh", "fish"}

// newCompletionInstallCmd adds `install` under cobra's own auto-generated
// `completion` command (see wireCompletionInstall in root.go). Generating the
// script is already solved by cobra's bash/zsh/fish/powershell subcommands;
// what's missing on macOS is knowing *where* each shell actually loads a
// script from, so you're not left copy-pasting `source <(...)` into an rc
// file by hand.
func newCompletionInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install [bash|zsh|fish]",
		Short: "Write the completion script to where your shell actually loads it from",
		Long: `Detects your shell from $SHELL if none is given, generates its completion
script (same content as ` + "`oak-dev completion <shell>`" + `), and writes it straight
to the file that shell loads on startup:

  fish   ~/.config/fish/completions/oak-dev.fish - fish loads this
         automatically, no further setup.
  zsh    $(brew --prefix)/share/zsh/site-functions/_oak-dev if Homebrew is on
         PATH - already on fpath, since Homebrew wires that up itself.
         Otherwise ~/.zfunc/_oak-dev, which needs one manual addition to
         ~/.zshrc (printed after writing).
  bash   $(brew --prefix)/etc/bash_completion.d/oak-dev if Homebrew is on
         PATH, otherwise ~/.local/share/bash-completion/completions/oak-dev.
         Either way this needs the bash-completion package sourced in your
         shell - printed as a reminder, same as the oak CLI check in
         ` + "`oak-dev doctor`" + `.

powershell isn't handled here - see ` + "`oak-dev completion powershell --help`" + `.

This only ever writes the one completion script named above; it never edits
an rc file for you.`,
		Example: `  oak-dev completion install          # shell detected from $SHELL
  oak-dev completion install fish
  oak-dev completion install zsh`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var out []string
			for _, s := range supportedInstallShells {
				if strings.HasPrefix(s, toComplete) {
					out = append(out, s)
				}
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := ""
			if len(args) == 1 {
				shell = args[0]
			} else {
				shell = detectShell()
				if shell == "" {
					return fmt.Errorf("couldn't detect your shell from $SHELL - name it explicitly, e.g. `oak-dev completion install fish`")
				}
				fmt.Printf("oak-dev: detected %s from $SHELL\n", shell)
			}

			path, note, err := completionTarget(shell)
			if err != nil {
				return err
			}

			var buf bytes.Buffer
			root := cmd.Root()
			switch shell {
			case "fish":
				err = root.GenFishCompletion(&buf, true)
			case "zsh":
				err = root.GenZshCompletion(&buf)
			case "bash":
				err = root.GenBashCompletionV2(&buf, true)
			}
			if err != nil {
				return fmt.Errorf("generating %s completion: %w", shell, err)
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				return err
			}

			fmt.Printf("oak-dev: wrote %s completions to %s\n", shell, path)
			if note != "" {
				fmt.Printf("oak-dev: %s\n", note)
			}
			fmt.Println("oak-dev: start a new shell for it to take effect.")
			return nil
		},
	}
	return cmd
}

// detectShell maps $SHELL to one of supportedInstallShells, or "" if it
// doesn't recognize it.
func detectShell() string {
	name := filepath.Base(os.Getenv("SHELL"))
	for _, s := range supportedInstallShells {
		if name == s {
			return s
		}
	}
	return ""
}

// completionTarget returns the file `install` should write shell's
// completion script to, and a one-time setup note to print afterward (empty
// if none is needed).
func completionTarget(shell string) (path, note string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}

	switch shell {
	case "fish":
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "fish", "completions", "oak-dev.fish"), "", nil

	case "zsh":
		if prefix, ok := brewPrefix(); ok {
			return filepath.Join(prefix, "share", "zsh", "site-functions", "_oak-dev"), "", nil
		}
		return filepath.Join(home, ".zfunc", "_oak-dev"),
			"one-time setup: add `fpath+=~/.zfunc` to ~/.zshrc before any `compinit` call " +
				"(and `autoload -Uz compinit && compinit` if you don't already run it)", nil

	case "bash":
		note := "needs the `bash-completion` package sourced in your shell - " +
			"`brew install bash-completion@2` if you don't have it"
		if prefix, ok := brewPrefix(); ok {
			return filepath.Join(prefix, "etc", "bash_completion.d", "oak-dev"), note, nil
		}
		return filepath.Join(home, ".local", "share", "bash-completion", "completions", "oak-dev"), note, nil

	default:
		return "", "", fmt.Errorf("unsupported shell %q - install supports %s "+
			"(see `oak-dev completion powershell --help` for powershell)",
			shell, strings.Join(supportedInstallShells, ", "))
	}
}

// brewPrefix reports Homebrew's prefix, so install can use the same
// locations cobra's own bash/zsh --help text suggests for macOS - both
// already on fpath / sourced automatically once Homebrew's shellenv is set
// up, which every prerequisite in `oak-dev doctor` already assumes.
func brewPrefix() (string, bool) {
	out, err := exec.Command("brew", "--prefix").Output()
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(string(out))
	return p, p != ""
}
