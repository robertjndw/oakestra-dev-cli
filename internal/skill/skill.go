// Package skill installs the oak-dev Agent Skill (skills/oak-dev, embedded
// into the binary via the repo-root oakdev.SkillFS so it can never drift from
// the source in this checkout) into whichever agent directories are in play:
// .claude/skills for Claude Code, .agents/skills for the portable convention
// read by Codex, Cursor, Gemini CLI, Copilot, Zed and OpenCode. See
// skills/oak-dev/SKILL.md for what the skill itself teaches an agent to do.
package skill

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	oakdev "oak-dev"
)

// Name is both the skill's directory name and its SKILL.md `name:` field -
// the Agent Skills spec (agentskills.io) requires the two to match.
const Name = "oak-dev"

// sourceRoot is where the embedded skill lives inside oakdev.SkillFS.
const sourceRoot = "skills/" + Name

// Agent identifies one of the directory conventions oak-dev can install
// into.
type Agent string

const (
	AgentClaude Agent = "claude" // .claude/skills - Claude Code only
	AgentAgents Agent = "agents" // .agents/skills - Codex, Cursor, Gemini CLI, Copilot, Zed, OpenCode
)

// Scope is what Base resolves a path against.
type Scope int

const (
	ScopeProject Scope = iota // the current directory
	ScopeGlobal               // $HOME
)

// Target is one directory Install/Status/Uninstall can act on.
type Target struct {
	Agent Agent
	Dir   string // <base>/.claude/skills/oak-dev or <base>/.agents/skills/oak-dev
}

// Base returns the root Targets are resolved against for scope: the current
// directory for ScopeProject, $HOME for ScopeGlobal.
func Base(scope Scope) (string, error) {
	if scope == ScopeProject {
		return os.Getwd()
	}
	return os.UserHomeDir()
}

// Resolve returns the Targets `selector` names at base: "claude" or "agents"
// for one directory convention, "all" for both, or "auto" (the default) to
// detect which agent(s) base already has evidence of.
//
// "auto" looks for a .claude directory (Claude Code) and, separately, for
// any of .agents/.codex/.cursor/.gemini/.config/opencode or an AGENTS.md file
// (the portable .agents/skills readers). If neither is found, both are
// installed - there is no strong signal either way, and writing to a
// directory nothing reads yet from is harmless.
func Resolve(base string, selector string) ([]Target, error) {
	switch selector {
	case "claude":
		return []Target{{AgentClaude, dirFor(base, AgentClaude)}}, nil
	case "agents":
		return []Target{{AgentAgents, dirFor(base, AgentAgents)}}, nil
	case "all":
		return []Target{
			{AgentClaude, dirFor(base, AgentClaude)},
			{AgentAgents, dirFor(base, AgentAgents)},
		}, nil
	case "auto", "":
		return autoDetect(base), nil
	default:
		return nil, fmt.Errorf("unknown skill target %q - use auto, claude, agents, or all", selector)
	}
}

func dirFor(base string, agent Agent) string {
	switch agent {
	case AgentClaude:
		return filepath.Join(base, ".claude", "skills", Name)
	case AgentAgents:
		return filepath.Join(base, ".agents", "skills", Name)
	default:
		panic("skill: unknown agent " + string(agent))
	}
}

func autoDetect(base string) []Target {
	wantClaude := exists(filepath.Join(base, ".claude"))
	wantAgents := existsAny(base, ".agents", ".codex", ".cursor", ".gemini", filepath.Join(".config", "opencode")) ||
		exists(filepath.Join(base, "AGENTS.md"))

	if !wantClaude && !wantAgents {
		wantClaude, wantAgents = true, true
	}

	var out []Target
	if wantClaude {
		out = append(out, Target{AgentClaude, dirFor(base, AgentClaude)})
	}
	if wantAgents {
		out = append(out, Target{AgentAgents, dirFor(base, AgentAgents)})
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func existsAny(base string, rel ...string) bool {
	for _, r := range rel {
		if exists(filepath.Join(base, r)) {
			return true
		}
	}
	return false
}

// files returns every file in the embedded skill, as slash-separated paths
// relative to the skill root (e.g. "SKILL.md", "references/commands.md").
// The embedded tree is fixed for the life of the binary, so the walk only
// ever runs once even though Install/Status are called once per target.
var (
	filesOnce sync.Once
	filesRels []string
	filesErr  error
)

func files() ([]string, error) {
	filesOnce.Do(func() {
		var out []string
		filesErr = fs.WalkDir(oakdev.SkillFS, sourceRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			out = append(out, strings.TrimPrefix(p, sourceRoot+"/"))
			return nil
		})
		if filesErr == nil {
			sort.Strings(out)
			filesRels = out
		}
	})
	return filesRels, filesErr
}

// Install writes the embedded skill into target.Dir, creating directories as
// needed. It reports which relative paths it actually wrote (missing or
// differing from what's on disk) versus left unchanged (already
// byte-identical) - safe to re-run at any time.
func Install(target Target) (wrote, unchanged []string, err error) {
	rels, err := files()
	if err != nil {
		return nil, nil, err
	}

	for _, rel := range rels {
		data, err := fs.ReadFile(oakdev.SkillFS, path.Join(sourceRoot, rel))
		if err != nil {
			return wrote, unchanged, err
		}

		dst := filepath.Join(target.Dir, filepath.FromSlash(rel))
		if same, _ := fileMatches(dst, data); same {
			unchanged = append(unchanged, rel)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return wrote, unchanged, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return wrote, unchanged, err
		}
		wrote = append(wrote, rel)
	}
	return wrote, unchanged, nil
}

func fileMatches(dst string, want []byte) (bool, error) {
	got, err := os.ReadFile(dst)
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, want), nil
}

// State classifies a Target against what the running binary would install.
type State int

const (
	NotInstalled State = iota
	UpToDate
	Modified
)

func (s State) String() string {
	switch s {
	case NotInstalled:
		return "not installed"
	case UpToDate:
		return "up to date"
	case Modified:
		return "differs from this binary's skill"
	default:
		return "unknown"
	}
}

// Status classifies target: NotInstalled if its directory doesn't exist,
// Modified if any embedded file is missing or differs on disk, UpToDate if
// every embedded file matches byte-for-byte.
func Status(target Target) (State, error) {
	if !exists(target.Dir) {
		return NotInstalled, nil
	}

	rels, err := files()
	if err != nil {
		return NotInstalled, err
	}
	for _, rel := range rels {
		data, err := fs.ReadFile(oakdev.SkillFS, path.Join(sourceRoot, rel))
		if err != nil {
			return NotInstalled, err
		}
		dst := filepath.Join(target.Dir, filepath.FromSlash(rel))
		same, err := fileMatches(dst, data)
		if err != nil || !same {
			return Modified, nil
		}
	}
	return UpToDate, nil
}

// Uninstall removes target.Dir. It only ever touches …/skills/oak-dev, so
// this never reaches a sibling skill even under --target all.
func Uninstall(target Target) error {
	if err := os.RemoveAll(target.Dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
