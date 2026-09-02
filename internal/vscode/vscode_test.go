package vscode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
)

func testConfig(t *testing.T, targetDir string) *config.Config {
	t.Helper()
	return &config.Config{
		RepoRoot:        targetDir,
		OakestraRepo:    filepath.Join(targetDir, "..", "oakestra"),
		OakestraNetRepo: filepath.Join(targetDir, "..", "oakestra-net"),
	}
}

// wantDebugTargetCount is the number of registry Targets with a DebugPort -
// one launch configuration and one hidden preLaunchTask apiece.
func wantDebugTargetCount(t *testing.T) int {
	t.Helper()
	n := 0
	for _, c := range components.All() {
		for _, tg := range c.Targets {
			if tg.DebugPort != 0 {
				n++
			}
		}
	}
	if n == 0 {
		t.Fatal("component registry has no targets with a DebugPort - is internal/components empty?")
	}
	return n
}

func TestGenerateCoversEveryDebugTargetExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)
	g := generate(cfg, dir)

	want := wantDebugTargetCount(t)
	if len(g.launch) != want {
		t.Fatalf("generated %d launch configs, want %d (one per registry target with a DebugPort)", len(g.launch), want)
	}

	seen := map[string]bool{}
	taskLabels := map[string]bool{}
	for _, e := range g.tasks {
		taskLabels[e.key] = true
	}

	for _, e := range g.launch {
		if seen[e.key] {
			t.Errorf("launch config name %q generated more than once", e.key)
		}
		seen[e.key] = true

		if !strings.HasPrefix(e.key, launchPrefix) {
			t.Errorf("launch config name %q does not carry the ownership prefix %q", e.key, launchPrefix)
		}

		var v struct {
			PreLaunchTask string `json:"preLaunchTask"`
		}
		if err := json.Unmarshal(e.data, &v); err != nil {
			t.Fatalf("unmarshaling generated config %q: %v", e.key, err)
		}
		if v.PreLaunchTask == "" {
			t.Errorf("launch config %q has no preLaunchTask", e.key)
		}
		if !taskLabels[v.PreLaunchTask] {
			t.Errorf("launch config %q references preLaunchTask %q, which was not generated", e.key, v.PreLaunchTask)
		}
	}

	for _, e := range g.tasks {
		if !strings.HasPrefix(e.key, taskPrefix) {
			t.Errorf("task label %q does not carry the ownership prefix %q", e.key, taskPrefix)
		}
	}
	for _, e := range g.inputs {
		if !strings.HasPrefix(e.key, inputPrefix) {
			t.Errorf("input id %q does not carry the ownership prefix %q", e.key, inputPrefix)
		}
	}
}

func TestGenerateGoConfigsUseDlvDap(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)
	g := generate(cfg, dir)

	found := 0
	for _, e := range g.launch {
		var v struct {
			Type         string `json:"type"`
			DebugAdapter string `json:"debugAdapter"`
		}
		if err := json.Unmarshal(e.data, &v); err != nil {
			t.Fatal(err)
		}
		if v.Type != "go" {
			continue
		}
		found++
		if v.DebugAdapter != "dlv-dap" {
			t.Errorf("%q: debugAdapter = %q, want dlv-dap", e.key, v.DebugAdapter)
		}
	}
	if found == 0 {
		t.Fatal("no Go launch configs were generated - is components.GoComponents() empty?")
	}
}

func TestGeneratePythonConfigsSetJustMyCodeFalseAndLocalRoot(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)
	g := generate(cfg, dir)

	byName := map[string]components.Component{}
	for _, c := range components.All() {
		byName[c.Name] = c
	}

	found := 0
	for _, e := range g.launch {
		var v struct {
			Type         string `json:"type"`
			JustMyCode   bool   `json:"justMyCode"`
			PathMappings []struct {
				LocalRoot  string `json:"localRoot"`
				RemoteRoot string `json:"remoteRoot"`
			} `json:"pathMappings"`
		}
		if err := json.Unmarshal(e.data, &v); err != nil {
			t.Fatal(err)
		}
		if v.Type != "debugpy" {
			continue
		}
		found++
		if v.JustMyCode {
			t.Errorf("%q: justMyCode = true, want false (breakpoints in the shared Oakestra libs must bind too)", e.key)
		}
		if len(v.PathMappings) != 1 {
			t.Fatalf("%q: pathMappings = %v, want exactly one", e.key, v.PathMappings)
		}
		if v.PathMappings[0].RemoteRoot != "/src" {
			t.Errorf("%q: remoteRoot = %q, want /src", e.key, v.PathMappings[0].RemoteRoot)
		}

		// The component name is embedded in the config's own name
		// ("oak-dev: attach <component>[ (<stack>)]"); recover it and check
		// localRoot against the one canonical join, cfg.SourceDir.
		name := strings.TrimPrefix(e.key, launchPrefix+"attach ")
		if i := strings.Index(name, " ("); i >= 0 {
			name = name[:i]
		}
		c, ok := byName[name]
		if !ok {
			t.Fatalf("%q: could not recover a component name from it", e.key)
		}
		want := relOrAbs(dir, cfg.SourceDir(c))
		if v.PathMappings[0].LocalRoot != want {
			t.Errorf("%q: localRoot = %q, want %q (cfg.SourceDir, not a hardcoded path)", e.key, v.PathMappings[0].LocalRoot, want)
		}
	}
	if found == 0 {
		t.Fatal("no Python launch configs were generated")
	}
}

func TestRelOrAbs(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		path string
		want string
	}{
		{"inside target dir", filepath.Join(dir, "sub", "file"), "${workspaceFolder}/sub/file"},
		{"target dir itself", dir, "${workspaceFolder}"},
		{"sibling directory", filepath.Join(dir, "..", "oakestra", "x"), filepath.Join(dir, "..", "oakestra", "x")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := relOrAbs(dir, c.path)
			// The sibling case compares against a non-cleaned path; clean
			// both sides so ".." segments don't cause a spurious mismatch.
			want := c.want
			if !strings.HasPrefix(want, "${workspaceFolder}") {
				want = filepath.Clean(want)
			}
			if got != want {
				t.Errorf("relOrAbs(%q, %q) = %q, want %q", dir, c.path, got, want)
			}
		})
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)

	res1, err := Install(cfg, dir, false)
	if err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if !res1.Launch.Changed || !res1.Tasks.Changed {
		t.Fatalf("first Install = %+v, want both files changed", res1)
	}

	res2, err := Install(cfg, dir, false)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if res2.Launch.Changed || res2.Tasks.Changed {
		t.Fatalf("second Install = %+v, want no changes (idempotent)", res2)
	}
}

func TestInstallDryRunTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)

	res, err := Install(cfg, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Launch.Changed || !res.Tasks.Changed {
		t.Fatalf("dry-run Install into an empty dir = %+v, want both reported as changed", res)
	}
	if _, err := os.Stat(filepath.Join(dir, ".vscode")); !os.IsNotExist(err) {
		t.Fatalf(".vscode was created despite dryRun: %v", err)
	}
}

func TestMergePreservesForeignEntriesAndUnknownTopLevelKeys(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)
	vdir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}

	launch := `{
  "version": "0.2.0",
  "configurations": [
    {"name": "Mine", "type": "node", "request": "launch"},
    {"name": "oak-dev: attach stale_component", "type": "go", "request": "attach"}
  ],
  "compounds": [{"name": "Everything", "configurations": ["Mine"]}]
}`
	if err := os.WriteFile(filepath.Join(vdir, "launch.json"), []byte(launch), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(cfg, dir, false); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(vdir, "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONC(data), &m); err != nil {
		t.Fatalf("merged launch.json is not valid jsonc: %v\n%s", err, data)
	}

	if _, ok := m["compounds"]; !ok {
		t.Error("unknown top-level key \"compounds\" was dropped by the merge")
	}

	var configs []json.RawMessage
	if err := json.Unmarshal(m["configurations"], &configs); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, raw := range configs {
		names = append(names, extractKey(raw, "name"))
	}
	if !slices.Contains(names, "Mine") {
		t.Errorf("configurations = %v, want it to still contain the foreign \"Mine\" entry", names)
	}
	if slices.Contains(names, "oak-dev: attach stale_component") {
		t.Errorf("configurations = %v, want the stale oak-dev entry replaced", names)
	}
	if !slices.Contains(names, "oak-dev: attach system_manager") {
		t.Errorf("configurations = %v, want a freshly generated oak-dev entry", names)
	}
}

func TestUninstallRemovesOwnedEntriesOnlyAndDeletesEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)

	if _, err := Install(cfg, dir, false); err != nil {
		t.Fatal(err)
	}
	launchPath := filepath.Join(dir, ".vscode", "launch.json")
	tasksPath := filepath.Join(dir, ".vscode", "tasks.json")

	// Nothing foreign was added, so uninstalling should delete both files
	// outright.
	if _, err := Uninstall(dir); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Errorf("launch.json still exists after Uninstall with nothing foreign in it: %v", err)
	}
	if _, err := os.Stat(tasksPath); !os.IsNotExist(err) {
		t.Errorf("tasks.json still exists after Uninstall with nothing foreign in it: %v", err)
	}

	// Re-install, then hand-add a foreign entry to each file - this time
	// Uninstall must strip only the oak-dev entries and keep the files.
	if _, err := Install(cfg, dir, false); err != nil {
		t.Fatal(err)
	}
	addForeignConfig(t, launchPath)
	addForeignTask(t, tasksPath)

	if _, err := Uninstall(dir); err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
	if _, err := os.Stat(launchPath); err != nil {
		t.Fatalf("launch.json was deleted despite a surviving foreign entry: %v", err)
	}
	if _, err := os.Stat(tasksPath); err != nil {
		t.Fatalf("tasks.json was deleted despite a surviving foreign entry: %v", err)
	}

	data, err := os.ReadFile(launchPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONC(data), &m); err != nil {
		t.Fatal(err)
	}
	var configs []json.RawMessage
	if err := json.Unmarshal(m["configurations"], &configs); err != nil {
		t.Fatal(err)
	}
	for _, raw := range configs {
		if strings.HasPrefix(extractKey(raw, "name"), launchPrefix) {
			t.Errorf("an oak-dev-owned config survived Uninstall: %s", raw)
		}
	}
	if len(configs) != 1 {
		t.Errorf("configurations after Uninstall = %v, want just the foreign one", configs)
	}
}

// TestUninstallKeepsFileWithForeignTopLevelKeyEvenWithNoForeignEntries covers
// a file where every "configurations"/"tasks"/"inputs" entry happens to be
// oak-dev's own, but a foreign top-level key - a hand-written "compounds"
// section, the one launch.json actually supports - is not. Uninstall must not
// delete the whole file in that case: doing so would silently discard content
// Uninstall never touched and has no way to regenerate.
func TestUninstallKeepsFileWithForeignTopLevelKeyEvenWithNoForeignEntries(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)

	if _, err := Install(cfg, dir, false); err != nil {
		t.Fatal(err)
	}
	launchPath := filepath.Join(dir, ".vscode", "launch.json")

	data, err := os.ReadFile(launchPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONC(data), &m); err != nil {
		t.Fatal(err)
	}
	m["compounds"] = json.RawMessage(`[{"name": "Everything", "configurations": ["oak-dev: attach system_manager"]}]`)
	doc, err := writeDoc(launchHeader, m, []string{"version", "configurations", "compounds"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launchPath, doc, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Uninstall(dir); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	data, err = os.ReadFile(launchPath)
	if err != nil {
		t.Fatalf("launch.json was deleted despite a surviving foreign top-level \"compounds\" key: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONC(data), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["compounds"]; !ok {
		t.Error("foreign top-level \"compounds\" key was dropped by Uninstall")
	}
	var configs []json.RawMessage
	if err := json.Unmarshal(got["configurations"], &configs); err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Errorf("configurations after Uninstall = %v, want none (all were oak-dev's own)", configs)
	}
}

// TestUninstallKeepsFileWithCommentEvenWithNoForeignEntries covers a file
// whose only foreign content is a hand-written comment. readJSONMap's jsonc
// parse discards comments before onlyKeys ever sees them, so without the
// hadComments check, "configurations" holding nothing but oak-dev's own
// entries would look safe to delete - and the delete would take the user's
// comment with it.
func TestUninstallKeepsFileWithCommentEvenWithNoForeignEntries(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)

	if _, err := Install(cfg, dir, false); err != nil {
		t.Fatal(err)
	}
	launchPath := filepath.Join(dir, ".vscode", "launch.json")

	data, err := os.ReadFile(launchPath)
	if err != nil {
		t.Fatal(err)
	}
	commented := append([]byte("// don't touch the ports below, they match the debug overlays\n"), data...)
	if err := os.WriteFile(launchPath, commented, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Uninstall(dir)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !res.Launch.HadComments {
		t.Error("res.Launch.HadComments = false, want true")
	}
	if res.Launch.Removed {
		t.Error("res.Launch.Removed = true, want the file kept because of its comment")
	}
	if _, err := os.Stat(launchPath); err != nil {
		t.Fatalf("launch.json was deleted despite a hand-written comment: %v", err)
	}
}

// TestWouldDiscardComments covers the preflight check the `vscode uninstall`
// command uses to ask for confirmation before Uninstall commits to a lossy
// rewrite - it has to see the same thing Uninstall itself would, without
// writing anything.
func TestWouldDiscardComments(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)

	launch, tasks, err := WouldDiscardComments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if launch || tasks {
		t.Errorf("WouldDiscardComments before install = (%v, %v), want (false, false)", launch, tasks)
	}

	if _, err := Install(cfg, dir, false); err != nil {
		t.Fatal(err)
	}
	launch, tasks, err = WouldDiscardComments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if launch || tasks {
		t.Errorf("WouldDiscardComments on a freshly installed, comment-free pair = (%v, %v), want (false, false)", launch, tasks)
	}

	launchPath := filepath.Join(dir, ".vscode", "launch.json")
	data, err := os.ReadFile(launchPath)
	if err != nil {
		t.Fatal(err)
	}
	commented := append([]byte("// keep this\n"), data...)
	if err := os.WriteFile(launchPath, commented, 0o644); err != nil {
		t.Fatal(err)
	}

	launch, tasks, err = WouldDiscardComments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !launch {
		t.Error("WouldDiscardComments launch = false, want true after adding a comment")
	}
	if tasks {
		t.Error("WouldDiscardComments tasks = true, want false - tasks.json was untouched")
	}
}

func addForeignConfig(t *testing.T, launchPath string) {
	t.Helper()
	data, err := os.ReadFile(launchPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONC(data), &m); err != nil {
		t.Fatal(err)
	}
	var configs []json.RawMessage
	if err := json.Unmarshal(m["configurations"], &configs); err != nil {
		t.Fatal(err)
	}
	configs = append(configs, json.RawMessage(`{"name": "Mine", "type": "node", "request": "launch"}`))
	m["configurations"] = rawArray(configs)
	doc, err := writeDoc(launchHeader, m, []string{"version", "configurations"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launchPath, doc, 0o644); err != nil {
		t.Fatal(err)
	}
}

func addForeignTask(t *testing.T, tasksPath string) {
	t.Helper()
	data, err := os.ReadFile(tasksPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONC(data), &m); err != nil {
		t.Fatal(err)
	}
	var tasks []json.RawMessage
	if err := json.Unmarshal(m["tasks"], &tasks); err != nil {
		t.Fatal(err)
	}
	tasks = append(tasks, json.RawMessage(`{"label": "Mine", "type": "shell", "command": "echo hi"}`))
	m["tasks"] = rawArray(tasks)
	doc, err := writeDoc(tasksHeader, m, []string{"version", "tasks", "inputs"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tasksPath, doc, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusClassification(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)

	s, err := Status(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s != NotInstalled {
		t.Fatalf("Status before install = %v, want NotInstalled", s)
	}

	if _, err := Install(cfg, dir, false); err != nil {
		t.Fatal(err)
	}
	s, err = Status(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s != UpToDate {
		t.Fatalf("Status after install = %v, want UpToDate", s)
	}

	launchPath := filepath.Join(dir, ".vscode", "launch.json")
	if err := os.WriteFile(launchPath, []byte(`{"version": "0.2.0", "configurations": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = Status(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s != Modified {
		t.Fatalf("Status after tampering = %v, want Modified", s)
	}
}
