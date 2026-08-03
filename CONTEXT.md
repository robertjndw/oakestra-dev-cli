# CONTEXT

The words this repo uses, and what each one means here. Terms are defined once;
if a name in the code disagrees with this file, one of the two is wrong.

`AGENTS.md` covers how to change the repo and why the compose overrides exist.
This file is only the vocabulary.

## Domain

**Component** - one editable piece of Oakestra source, and an entry in the
registry in `internal/components`. `system_manager`, `scheduler`, `nodeengine`,
`netmanager` are components. A component knows its language, which checkout its
source lives in, how to build it, and which containers it runs as. Not every
container is a component: `mongo_root`, `root_redis` and `mqtt` have no entry.

**Target** - one running instance of a component: a single container in a single
stack. Most components have one; `scheduler` has two (`root_scheduler`,
`cluster_scheduler`) sharing one cross-compiled binary. A target carries the
things that differ per container - the live and debug overlays, the debug port,
and whether it must be restarted in place.

**Stack** - one of the three compose projects: `root`, `cluster`, `worker`. They
join one shared docker network, so containers resolve each other by name across
projects.

**Scope** - which stacks a command acts on: `full`, or a single stack. Set by
`--stack`, sticky in `.generated/stack` after an `up`, and printed by every
command that does work. `worker` scope implies `cluster`, because the worker
needs something to register with.

**Live** - a component whose container runs from your working tree rather than
its baked image. Python components run under `gunicorn --reload`; Go components
run a cross-compiled binary bind-mounted at `/oak-bin`. The live set lives in
`oak-dev.yaml` and `reload`/`debug` add to it themselves.

**In-place restart** - restarting a process *inside* a container instead of
recreating the container. Only `nodeengine` and `netmanager` need it, and the
reason is load-bearing: recreating the worker mints a new hostname, which
`cluster_manager` registers as a new node ID, stranding every already-scheduled
instance in `NODE_SCHEDULED` forever.

**Endpoint** - a named thing that lives in a container but wants a specific
client rather than a shell: `mongo-root`, `mongo-cluster`, `mqtt`.

## Architecture

The vocabulary below is the deep-module one: **module**, **interface**,
**implementation**, **depth**, **seam**, **adapter**, **leverage**,
**locality**. A module is deep when a lot of behaviour sits behind a small
interface; a seam is where you can change behaviour without editing in place.

**Runner** (`internal/proc`) - the low seam over `os/exec`. Every external
program oak-dev starts - docker, go, python3, pytest - goes through it. Two
adapters: `OS` starts real processes, `Recorder` starts nothing, remembers every
command in order, and answers with canned output. Without it, nothing that
orchestrates docker could be tested at all.

**Spec** (`internal/proc`) - one external command described as a value: program,
arguments, working directory, environment, and whether it wants stdin or
combined output. A Spec can be recorded, compared and printed without anything
having run. It is also what `LogSpec` hands to `multilog`: compose describes the
command, multilog supervises it.

**Compose** (`internal/compose`) - the intent seam over `docker compose`.
Callers name what they want done - `Recreate`, `Restart`, `Build`, `Exec`,
`Capture`, `StackUp`, `StackDown` - and the module owns the `-f` chain and
docker's argument vocabulary. Built on a Runner, so it has its own test surface
for argv construction.

Distinguish the module from the program: **Compose** (capitalised, this
interface) is oak-dev's; `docker compose` is the tool behind it.

**Ref** (`internal/compose`) - the pair Compose addresses a container by: a
stack, and a compose service name. Deliberately narrower than a Target, because
the endpoints and infrastructure containers have no registry entry and could not
otherwise be named.

**Chain** - the ordered `-f` file list for one stack: the upstream compose file,
the profile opt-outs, this repo's overrides, then the live and debug overlays.
Computed by `topology.Chains`, bound to a Compose at construction. `Render` is
`Chains` plus the `.generated/` artifacts; completion uses `Chains` so pressing
TAB does not write files.

## Testing

**Golden argv test** - pins the exact commands a code path issues, so a change
to how oak-dev talks to docker has to be made deliberately. These exist because
a wrong argument does not fail loudly: it looks like success while serving a
stale binary or leaving a debug port unpublished.

**Hazard test** - asserts one of the invariants in `AGENTS.md` at the level of
intent rather than argument strings: nothing recreated the worker, a failed
build issued no restart, a recreate carried every debug overlay already on that
container.
