package compose

import (
	"fmt"
	"slices"

	"oak-dev/internal/proc"
)

// Ref is one addressable container: a compose service, and the stack whose
// project declares it.
//
// It is deliberately narrower than components.Target: the endpoints
// (mongo_root, mqtt) and the infrastructure containers (root_redis,
// mongo_rootnet) have no entry in the component registry at all, so anything
// requiring one could not name them.
type Ref struct {
	Stack     string
	Container string
}

// LogOpts controls what LogSpec asks compose for.
type LogOpts struct {
	Tail   int
	Follow bool
}

// Compose is the seam every command crosses to act on containers. Callers name
// what they want done; which -f chain that needs, and how docker spells it,
// live behind here.
//
// Refs may span stacks: the scheduler runs in both the root and cluster
// projects, so one Restart of it is really two compose invocations.
// Implementations group by stack and emit one command per stack; that's also
// how reset does its single multi-container restart.
type Compose interface {
	// Recreate brings containers up against the declared configuration.
	// Compose only recreates when the effective config actually differs, so
	// this is a no-op in the normal case - and is what undoes `oak-dev debug`,
	// whose overlays change the entrypoint and so survive a plain restart.
	Recreate(refs ...Ref) error
	// ForceRecreate recreates unconditionally. Needed when the change is in an
	// overlay compose cannot see as a config difference.
	ForceRecreate(refs ...Ref) error
	Restart(refs ...Ref) error
	Build(refs ...Ref) error

	// Exec runs a command in a container with the terminal attached.
	Exec(r Ref, argv ...string) error
	// Capture runs a command in a container with no TTY and returns its
	// output, stderr included: every caller reports it back attached to an
	// error, and stderr is the part worth reading.
	Capture(r Ref, argv ...string) ([]byte, error)

	// StackUp starts a whole project, building images as needed.
	StackUp(stack string, extra ...string) error
	// StackDown stops a whole project, optionally deleting its volumes.
	StackDown(stack string, volumes bool) error
	// StackCapture runs a compose subcommand against a whole project, for the
	// questions that are about the project rather than a container -
	// `config --services`.
	StackCapture(stack string, argv ...string) ([]byte, error)

	// LogSpec and ExecSpec describe a long-running command without starting
	// it, for multilog to supervise. Compose stays the only module that knows
	// docker's argv; multilog stays the only one that knows process
	// supervision.
	LogSpec(r Ref, o LogOpts) (proc.Spec, error)
	ExecSpec(r Ref, argv ...string) (proc.Spec, error)

	// WithOverlays returns a Compose whose chain for one stack carries extra
	// files. `oak-dev debug` layers its override-debug-*.yml this way; the
	// receiver is left unchanged.
	WithOverlays(stack string, paths ...string) Compose
}

// chains binds a rendered topology to a Client. Resolving a Ref to its -f
// chain happens here instead of in each caller, so the twelve
// `files[t.Stack]` lookups (and their two different spellings of the same nil
// check) collapse into one error in one place.
func (c *Client) chain(stack string) ([]string, error) {
	base, ok := c.topo[stack]
	if !ok {
		return nil, fmt.Errorf("the %s stack is not in the current scope (%s)", stack, c.cfg.Stack)
	}
	if extra := c.overlays[stack]; len(extra) > 0 {
		return append(slices.Clone(base), extra...), nil
	}
	return base, nil
}

// byStack groups refs into one batch per stack, preserving the order stacks
// were first named so the emitted commands are stable between runs.
func byStack(refs []Ref) ([]string, map[string][]string) {
	var order []string
	groups := map[string][]string{}
	for _, r := range refs {
		if _, seen := groups[r.Stack]; !seen {
			order = append(order, r.Stack)
		}
		if !slices.Contains(groups[r.Stack], r.Container) {
			groups[r.Stack] = append(groups[r.Stack], r.Container)
		}
	}
	return order, groups
}

// perStack runs one compose invocation per stack represented in refs, with the
// stack's containers appended to args.
func (c *Client) perStack(refs []Ref, args ...string) error {
	order, groups := byStack(refs)
	for _, stack := range order {
		files, err := c.chain(stack)
		if err != nil {
			return err
		}
		if err := c.run(files, append(slices.Clone(args), groups[stack]...)...); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Recreate(refs ...Ref) error {
	return c.perStack(refs, "up", "-d")
}

func (c *Client) ForceRecreate(refs ...Ref) error {
	return c.perStack(refs, "up", "-d", "--force-recreate")
}

func (c *Client) Restart(refs ...Ref) error {
	return c.perStack(refs, "restart")
}

func (c *Client) Build(refs ...Ref) error {
	return c.perStack(refs, "build")
}

func (c *Client) Exec(r Ref, argv ...string) error {
	files, err := c.chain(r.Stack)
	if err != nil {
		return err
	}
	return c.run(files, append([]string{"exec", r.Container}, argv...)...)
}

func (c *Client) Capture(r Ref, argv ...string) ([]byte, error) {
	files, err := c.chain(r.Stack)
	if err != nil {
		return nil, err
	}
	return c.output(files, append([]string{"exec", "-T", r.Container}, argv...)...)
}

func (c *Client) StackUp(stack string, extra ...string) error {
	files, err := c.chain(stack)
	if err != nil {
		return err
	}
	return c.run(files, append([]string{"up", "-d", "--build"}, extra...)...)
}

func (c *Client) StackDown(stack string, volumes bool) error {
	files, err := c.chain(stack)
	if err != nil {
		return err
	}
	args := []string{"down"}
	if volumes {
		args = append(args, "-v")
	}
	return c.run(files, args...)
}

func (c *Client) StackCapture(stack string, argv ...string) ([]byte, error) {
	files, err := c.chain(stack)
	if err != nil {
		return nil, err
	}
	return c.output(files, argv...)
}

func (c *Client) LogSpec(r Ref, o LogOpts) (proc.Spec, error) {
	files, err := c.chain(r.Stack)
	if err != nil {
		return proc.Spec{}, err
	}
	args := []string{"logs", fmt.Sprintf("--tail=%d", o.Tail)}
	if o.Follow {
		args = append(args, "-f")
	}
	args = append(args, r.Container)
	return c.spec(files, args...), nil
}

func (c *Client) ExecSpec(r Ref, argv ...string) (proc.Spec, error) {
	files, err := c.chain(r.Stack)
	if err != nil {
		return proc.Spec{}, err
	}
	return c.spec(files, append([]string{"exec", "-T", r.Container}, argv...)...), nil
}

func (c *Client) WithOverlays(stack string, paths ...string) Compose {
	next := *c
	next.overlays = map[string][]string{}
	for k, v := range c.overlays {
		next.overlays[k] = v
	}
	next.overlays[stack] = append(slices.Clone(next.overlays[stack]), paths...)
	return &next
}

// StackLogSpec is the whole-stack form of LogSpec, for `logs` and `dev` with
// no target named.
func (c *Client) StackLogSpec(stack string, o LogOpts) (proc.Spec, error) {
	files, err := c.chain(stack)
	if err != nil {
		return proc.Spec{}, err
	}
	args := []string{"logs"}
	if o.Follow {
		args = append(args, "-f")
	}
	args = append(args, fmt.Sprintf("--tail=%d", o.Tail))
	return c.spec(files, args...), nil
}

// Stacks reports which stacks this Compose can act on, in start order - the
// scope, as the topology actually rendered it.
func (c *Client) Stacks() []string {
	var out []string
	for _, s := range stackOrder {
		if _, ok := c.topo[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

var _ Compose = (*Client)(nil)
