package vscode

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/target"
)

// launchPrefix and taskPrefix mark the "name"/"label" of an entry oak-dev
// owns: generated fresh on every `vscode install`, safe to overwrite because
// nothing but the generator is expected to spell a config that way. inputPrefix
// does the same job for an input's "id" (camelCase, since ids can't contain
// spaces or colons).
const (
	launchPrefix = "oak-dev: "
	taskPrefix   = "oak-dev: "
	inputPrefix  = "oakDev"
)

const (
	launchVersion = "0.2.0"
	tasksVersion  = "2.0.0"
)

// entry is one array element - a launch configuration, a task, or an input -
// paired with the key (name/label/id) merge and uninstall use to recognize
// it as oak-dev's own.
type entry struct {
	key  string
	data json.RawMessage
}

// presentation is shared by launch configurations (sorts/groups/hides them in
// the Debug dropdown) and tasks (splits their terminals into one group) - the
// field name and shape happen to be identical in both schemas.
type presentation struct {
	Group string `json:"group,omitempty"`
	Panel string `json:"panel,omitempty"`
}

var oakDevGroup = presentation{Group: "oak-dev"}

// --- launch.json ------------------------------------------------------

type goAttach struct {
	Name          string       `json:"name"`
	Type          string       `json:"type"`
	DebugAdapter  string       `json:"debugAdapter"`
	Request       string       `json:"request"`
	Mode          string       `json:"mode"`
	Host          string       `json:"host"`
	Port          int          `json:"port"`
	PreLaunchTask string       `json:"preLaunchTask"`
	Presentation  presentation `json:"presentation"`
}

type pathMapping struct {
	LocalRoot  string `json:"localRoot"`
	RemoteRoot string `json:"remoteRoot"`
}

type connectSpec struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type pyAttach struct {
	Name          string        `json:"name"`
	Type          string        `json:"type"`
	Request       string        `json:"request"`
	Connect       connectSpec   `json:"connect"`
	PathMappings  []pathMapping `json:"pathMappings"`
	JustMyCode    bool          `json:"justMyCode"`
	PreLaunchTask string        `json:"preLaunchTask"`
	Presentation  presentation  `json:"presentation"`
}

// --- tasks.json ---------------------------------------------------------

type taskOptions struct {
	Cwd string `json:"cwd"`
}

type taskGroup struct {
	Kind      string `json:"kind"`
	IsDefault bool   `json:"isDefault"`
}

type runOptions struct {
	ReevaluateOnRerun bool `json:"reevaluateOnRerun"`
}

type task struct {
	Label          string       `json:"label"`
	Type           string       `json:"type"`
	Command        string       `json:"command"`
	Args           []string     `json:"args,omitempty"`
	Options        taskOptions  `json:"options"`
	Group          *taskGroup   `json:"group,omitempty"`
	Presentation   presentation `json:"presentation"`
	ProblemMatcher []string     `json:"problemMatcher"`
	IsBackground   bool         `json:"isBackground,omitempty"`
	Hide           bool         `json:"hide,omitempty"`
	RunOptions     *runOptions  `json:"runOptions,omitempty"`
}

type pickOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type input struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Description string       `json:"description"`
	Options     []pickOption `json:"options"`
}

// generated is everything Generate produces for one target directory.
type generated struct {
	launch []entry
	tasks  []entry
	inputs []entry
}

// relOrAbs expresses path relative to targetDir as ${workspaceFolder}/... so
// a config installed alongside the source it debugs stays portable, or as an
// absolute path when path falls outside targetDir entirely - the common case
// for a config installed into $OAKESTRA_REPO or $OAKESTRA_NET_REPO, which
// still needs to reach back into the oakestra-dev-cli checkout to run
// `oak-dev` at all.
func relOrAbs(targetDir, path string) string {
	rel, err := filepath.Rel(targetDir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	if rel == "." {
		return "${workspaceFolder}"
	}
	return "${workspaceFolder}/" + filepath.ToSlash(rel)
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		// Every value passed here is a struct literal built entirely from
		// this package's own types - a marshal failure would mean a field
		// holds something JSON genuinely can't encode (a channel, a func),
		// which is a bug in the generator, not a runtime condition to
		// recover from.
		panic(fmt.Sprintf("oak-dev/internal/vscode: marshaling %#v: %v", v, err))
	}
	return data
}

// componentLabel is the pickString option label for a component: its name,
// plus its short aliases in parens so the picker teaches them at the same
// time it's used.
func componentLabel(c components.Component) string {
	if len(c.Aliases) == 0 {
		return c.Name
	}
	return c.Name + " (" + strings.Join(c.Aliases, ", ") + ")"
}

// debugTargets pairs each Target that has a debugger with whether its owning
// component needs --stack to disambiguate it (true exactly when the
// component has more than one such target - currently just scheduler, split
// across the root and cluster stacks).
type debugTarget struct {
	c     components.Component
	t     components.Target
	multi bool
}

func allDebugTargets() []debugTarget {
	var out []debugTarget
	for _, c := range components.All() {
		var withPort []components.Target
		for _, t := range c.Targets {
			if t.DebugPort != 0 {
				withPort = append(withPort, t)
			}
		}
		multi := len(withPort) > 1
		for _, t := range withPort {
			out = append(out, debugTarget{c: c, t: t, multi: multi})
		}
	}
	return out
}

// launchName is the "oak-dev: attach <component>" name shown in the Debug
// dropdown, disambiguated with the stack for a component (scheduler) that
// debugs differently per stack.
func (d debugTarget) launchName() string {
	if d.multi {
		return launchPrefix + "attach " + d.c.Name + " (" + d.t.Stack + ")"
	}
	return launchPrefix + "attach " + d.c.Name
}

// debugTaskLabel is the preLaunchTask that runs `oak-dev debug` for this
// exact target before the debugger attaches. It's left visible (not Hide:
// true) so it doubles as the general-purpose "run oak-dev debug on some
// component" command - a picker-driven equivalent would need to prompt for
// component and stack together, which pickString can't express as two
// correlated values, and a stack-unaware picker is exactly the ambiguous/
// out-of-scope failure this per-target task avoids.
func (d debugTarget) debugTaskLabel() string {
	if d.multi {
		return taskPrefix + "debug " + d.c.Name + " (" + d.t.Stack + ")"
	}
	return taskPrefix + "debug " + d.c.Name
}

// debugTaskArgs always pins --stack to this target's own stack: sticky scope
// (see AGENTS.md) means the CLI's ambient scope at the moment VS Code runs
// this task may not match the target this launch config was generated for
// (e.g. `up --stack worker` left cluster/worker in scope, but this task
// debugs a root-only component) - pinning removes that dependency entirely,
// not just for the one component (scheduler) that's ambiguous within a
// single scope.
func (d debugTarget) debugTaskArgs() []string {
	return []string{"debug", d.c.Name, "--stack", d.t.Stack, "--wait"}
}

// generate builds every entry oak-dev owns for targetDir: launch
// configurations plus their preLaunchTasks, the general-purpose command
// tasks, and the pickString inputs those tasks prompt with. Nothing here
// touches disk - Install/Status feed the result through mergeLaunch/
// mergeTasks, which is what actually reads and writes .vscode/*.json.
func generate(cfg *config.Config, targetDir string) generated {
	repoRootRef := relOrAbs(targetDir, cfg.RepoRoot)

	var g generated

	for _, d := range allDebugTargets() {
		taskLabel := d.debugTaskLabel()
		if d.c.Kind == components.KindGo {
			g.launch = append(g.launch, entry{d.launchName(), mustJSON(goAttach{
				Name:          d.launchName(),
				Type:          "go",
				DebugAdapter:  "dlv-dap",
				Request:       "attach",
				Mode:          "remote",
				Host:          "127.0.0.1",
				Port:          d.t.DebugPort,
				PreLaunchTask: taskLabel,
				Presentation:  oakDevGroup,
			})})
		} else {
			localRoot := relOrAbs(targetDir, cfg.SourceDir(d.c))
			g.launch = append(g.launch, entry{d.launchName(), mustJSON(pyAttach{
				Name:    d.launchName(),
				Type:    "debugpy",
				Request: "attach",
				Connect: connectSpec{Host: "127.0.0.1", Port: d.t.DebugPort},
				PathMappings: []pathMapping{
					{LocalRoot: localRoot, RemoteRoot: "/src"},
				},
				JustMyCode:    false,
				PreLaunchTask: taskLabel,
				Presentation:  oakDevGroup,
			})})
		}

		g.tasks = append(g.tasks, entry{taskLabel, mustJSON(task{
			Label:          taskLabel,
			Type:           "process",
			Command:        "oak-dev",
			Args:           d.debugTaskArgs(),
			Options:        taskOptions{Cwd: repoRootRef},
			Presentation:   oakDevGroup,
			ProblemMatcher: []string{},
		})})
	}

	g.tasks = append(g.tasks, generalTasks(repoRootRef)...)
	g.inputs = append(g.inputs, inputs()...)

	return g
}

// generalTasks is the fixed set of tasks that aren't tied to any one
// component's debug session - the everyday oak-dev commands, plus the ones
// that take a component/target through a pickString input.
func generalTasks(cwd string) []entry {
	plain := func(label string, args ...string) task {
		return task{
			Label:          taskPrefix + label,
			Type:           "process",
			Command:        "oak-dev",
			Args:           args,
			Options:        taskOptions{Cwd: cwd},
			Presentation:   oakDevGroup,
			ProblemMatcher: []string{},
		}
	}
	withInput := func(t task) task {
		t.RunOptions = &runOptions{ReevaluateOnRerun: false}
		return t
	}

	// Debugging a component has no generic picker-driven task here: it needs
	// both a component and a stack, and pickString can't express two
	// correlated values from one selection. The "oak-dev: debug <component>"
	// tasks generated per debug target (see generate) cover that instead,
	// visibly.
	tasks := []task{
		plain("up", "up"),
		plain("down", "down"),
		func() task {
			t := plain("dev", "dev")
			t.IsBackground = true
			t.Presentation.Panel = "dedicated"
			return t
		}(),
		plain("status", "status"),
		plain("reset", "reset", "--yes"),
		plain("doctor --fix", "doctor", "--fix"),
		func() task {
			t := plain("test", "test")
			t.Group = &taskGroup{Kind: "test", IsDefault: true}
			return t
		}(),
		plain("test --smoke", "test", "--smoke"),
		withInput(plain("reload", "reload", "${input:"+idComponent+"}")),
		withInput(plain("test (go)", "test", "${input:"+idGoComponent+"}")),
		withInput(func() task {
			t := plain("logs", "logs", "${input:"+idLogTarget+"}")
			t.IsBackground = true
			t.Presentation.Panel = "dedicated"
			return t
		}()),
		withInput(plain("shell", "shell", "${input:"+idShellTarget+"}")),
	}

	out := make([]entry, len(tasks))
	for i, t := range tasks {
		out[i] = entry{t.Label, mustJSON(t)}
	}
	return out
}

const (
	idComponent   = inputPrefix + "Component"
	idGoComponent = inputPrefix + "GoComponent"
	idLogTarget   = inputPrefix + "LogTarget"
	idShellTarget = inputPrefix + "ShellTarget"
)

// containerNames returns every component container name, deduplicated and in
// registry order - the set of `oak-dev logs`/`oak-dev shell` targets that
// name exactly one container.
func containerNames() []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range components.All() {
		for _, t := range c.Targets {
			if !seen[t.Container] {
				seen[t.Container] = true
				out = append(out, t.Container)
			}
		}
	}
	return out
}

func pickOptions(names []string) []pickOption {
	opts := make([]pickOption, len(names))
	for i, name := range names {
		opts[i] = pickOption{Label: name, Value: name}
	}
	return opts
}

// inputs builds the pickString inputs the general tasks above reference,
// sourced from the component/endpoint registries so a new component, stack,
// or endpoint shows up in the picker without touching this file by hand.
func inputs() []entry {
	componentOptions := func(cs []components.Component) []pickOption {
		opts := make([]pickOption, len(cs))
		for i, c := range cs {
			opts[i] = pickOption{Label: componentLabel(c), Value: c.Name}
		}
		return opts
	}

	containers := containerNames()

	// logs can follow several containers at once, so a stack (which expands
	// to all of its containers) is a valid choice here.
	logTargets := append(append([]string{}, components.Stacks()...), containers...)
	logTargets = append(logTargets, "mqtt")

	// shell needs exactly one container: a stack name would resolve to every
	// container in it and runShell would reject the selection outright, so
	// this list is containers and single-container endpoints only - never a
	// stack.
	shellTargets := append(append([]string{}, containers...), target.EndpointNames()...)

	in := []input{
		{
			ID:          idComponent,
			Type:        "pickString",
			Description: "oak-dev component",
			Options:     componentOptions(components.All()),
		},
		{
			ID:          idGoComponent,
			Type:        "pickString",
			Description: "oak-dev Go component (go test ./..., no containers)",
			Options:     componentOptions(components.GoComponents()),
		},
		{
			ID:          idLogTarget,
			Type:        "pickString",
			Description: "oak-dev logs target: a stack, component container, or mqtt",
			Options:     pickOptions(logTargets),
		},
		{
			ID:          idShellTarget,
			Type:        "pickString",
			Description: "oak-dev shell target: a component container or named endpoint",
			Options:     pickOptions(shellTargets),
		},
	}

	out := make([]entry, len(in))
	for i, v := range in {
		out[i] = entry{v.ID, mustJSON(v)}
	}
	return out
}
