package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolveTarget(t *testing.T) {
	cases := []struct {
		name     string
		selector string
		want     []Agent
		wantErr  bool
	}{
		{"claude", "claude", []Agent{AgentClaude}, false},
		{"agents", "agents", []Agent{AgentAgents}, false},
		{"all", "all", []Agent{AgentClaude, AgentAgents}, false},
		{"unknown", "bogus", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			targets, err := Resolve(t.TempDir(), c.selector)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) = nil error, want error", c.selector)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", c.selector, err)
			}
			var got []Agent
			for _, tg := range targets {
				got = append(got, tg.Agent)
			}
			if len(got) != len(c.want) {
				t.Fatalf("Resolve(%q) = %v, want %v", c.selector, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("Resolve(%q) = %v, want %v", c.selector, got, c.want)
				}
			}
		})
	}
}

func TestAutoDetect(t *testing.T) {
	cases := []struct {
		name   string
		mkdirs []string // relative dirs/files to create under base before detecting
		want   []Agent
	}{
		{"nothing detected installs both", nil, []Agent{AgentClaude, AgentAgents}},
		{"only .claude present", []string{".claude"}, []Agent{AgentClaude}},
		{"only .agents present", []string{".agents"}, []Agent{AgentAgents}},
		{"only .cursor present counts as agents", []string{".cursor"}, []Agent{AgentAgents}},
		{"AGENTS.md file counts as agents", nil, []Agent{AgentAgents}}, // file created specially below
		{"both present", []string{".claude", ".agents"}, []Agent{AgentClaude, AgentAgents}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			for _, d := range c.mkdirs {
				if err := os.Mkdir(filepath.Join(base, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if c.name == "AGENTS.md file counts as agents" {
				if err := os.WriteFile(filepath.Join(base, "AGENTS.md"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			targets := autoDetect(base)
			var got []Agent
			for _, tg := range targets {
				got = append(got, tg.Agent)
			}
			if len(got) != len(c.want) {
				t.Fatalf("autoDetect(%v) = %v, want %v", c.mkdirs, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("autoDetect(%v) = %v, want %v", c.mkdirs, got, c.want)
				}
			}
		})
	}
}

func TestInstallWritesAllFilesAndIsIdempotent(t *testing.T) {
	base := t.TempDir()
	target := Target{Agent: AgentClaude, Dir: dirFor(base, AgentClaude)}

	wantFiles, err := files()
	if err != nil {
		t.Fatal(err)
	}
	if len(wantFiles) == 0 {
		t.Fatal("embedded skill has no files - is skills/oak-dev populated?")
	}

	wrote, unchanged, err := Install(target)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(unchanged) != 0 {
		t.Fatalf("first install reported %d unchanged, want 0", len(unchanged))
	}
	if len(wrote) != len(wantFiles) {
		t.Fatalf("first install wrote %d files, want %d (%v)", len(wrote), len(wantFiles), wantFiles)
	}

	for _, rel := range wantFiles {
		dst := filepath.Join(target.Dir, filepath.FromSlash(rel))
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("expected %s to exist: %v", dst, err)
		}
	}

	// Re-running should be a no-op: nothing written, everything unchanged.
	wrote2, unchanged2, err := Install(target)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if len(wrote2) != 0 {
		t.Fatalf("second install wrote %v, want none", wrote2)
	}
	if len(unchanged2) != len(wantFiles) {
		t.Fatalf("second install reported %d unchanged, want %d", len(unchanged2), len(wantFiles))
	}
}

func TestStatusClassification(t *testing.T) {
	base := t.TempDir()
	target := Target{Agent: AgentClaude, Dir: dirFor(base, AgentClaude)}

	if s, err := Status(target); err != nil || s != NotInstalled {
		t.Fatalf("Status before install = %v, %v; want NotInstalled, nil", s, err)
	}

	if _, _, err := Install(target); err != nil {
		t.Fatal(err)
	}
	if s, err := Status(target); err != nil || s != UpToDate {
		t.Fatalf("Status after install = %v, %v; want UpToDate, nil", s, err)
	}

	skillMD := filepath.Join(target.Dir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, err := Status(target); err != nil || s != Modified {
		t.Fatalf("Status after edit = %v, %v; want Modified, nil", s, err)
	}
}

func TestUninstallRemovesDirAndIsSafeWhenAbsent(t *testing.T) {
	base := t.TempDir()
	target := Target{Agent: AgentClaude, Dir: dirFor(base, AgentClaude)}

	if _, _, err := Install(target); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(target); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(target.Dir); !os.IsNotExist(err) {
		t.Fatalf("target dir still exists after Uninstall: %v", err)
	}

	// Uninstalling something that was never installed must not error.
	if err := Uninstall(target); err != nil {
		t.Fatalf("Uninstall on absent dir: %v", err)
	}
}

// TestFrontmatter guards the Agent Skills spec (agentskills.io) at build
// time rather than only at install time: name must be kebab-case, match the
// skill's directory, and description must respect the format's constraints.
func TestFrontmatter(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", Name, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	body := string(data)
	if !strings.HasPrefix(body, "---\n") {
		t.Fatal("SKILL.md must start with a YAML frontmatter block (---)")
	}
	end := strings.Index(body[4:], "\n---")
	if end < 0 {
		t.Fatal("SKILL.md frontmatter block is not terminated with ---")
	}
	raw := body[4 : 4+end]

	var fm struct {
		Name          string `yaml:"name"`
		Description   string `yaml:"description"`
		License       string `yaml:"license"`
		Compatibility string `yaml:"compatibility"`
	}
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		t.Fatalf("parsing frontmatter: %v", err)
	}

	if fm.Name != Name {
		t.Errorf("frontmatter name = %q, want %q (must match the skill's directory name)", fm.Name, Name)
	}
	if len(fm.Name) > 64 {
		t.Errorf("name is %d chars, spec max is 64", len(fm.Name))
	}
	for _, r := range fm.Name {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Errorf("name %q contains %q - only lowercase letters, digits and hyphens are allowed", fm.Name, r)
			break
		}
	}

	if fm.Description == "" {
		t.Error("description must not be empty")
	}
	if len(fm.Description) > 1024 {
		t.Errorf("description is %d chars, spec max is 1024", len(fm.Description))
	}
	if strings.ContainsAny(fm.Description, "<>") {
		t.Error("description must not contain < or >")
	}

	if len(fm.Compatibility) > 500 {
		t.Errorf("compatibility is %d chars, spec max is 500", len(fm.Compatibility))
	}
}
