# Workflows

## First-time setup

```bash
make install                         # build oak-dev onto PATH (PREFIX=~/.local/bin)
cp .env.example .env                 # once
cp oak-dev.yaml.example oak-dev.yaml # once - pick live: components + stack
oak-dev doctor --fix                 # preflight + venv + protobuf stubs + oak CLI config
```

## Starting work

```bash
oak-dev dev                # start everything, watch for edits, stream logs
```

This is the default entry point for a session: it brings the stack up if
needed, watches every live Go component's source (cross-compile + in-place
restart on save; Python needs no watcher, already under `gunicorn --reload`),
and aggregates every container's logs into one stream. Ctrl-C stops
everything.

## Iterating on one component

```bash
oak-dev reload sched              # cross-compile + in-place restart (~5s), both stacks
oak-dev reload ne                 # same for the worker; restarts nodeengined, not the container
oak-dev reload cm                 # Python: already live under gunicorn --reload, a no-op
oak-dev reload sm --image         # full image rebuild (~30s), for requirements.txt/Dockerfile
oak-dev test sched                # go test ./..., no containers (~2s)
oak-dev dev sched                 # watch just the scheduler (logs still cover everything in scope)
```

`reload`/`debug` add the named component to `oak-dev.yaml`'s `live:` list
themselves and recreate its container once to attach the bind mount;
`--no-live` turns that automatic promotion back into an error, for when a
missing `live:` entry should be surprising.

## Verifying a change actually reached the running system

`oak-dev test --smoke` proves the stack is alive; it does not deploy anything.
To prove a change survives the whole chain (REST API -> root scheduler ->
cluster scheduler -> MQTT -> NodeEngine -> containerd), either run the
deployment tests or deploy by hand:

```bash
oak-dev test -- tests/test_03_deployment.py   # the scripted version
oak application create -f fixtures/nginx.json # the manual one (JSON, not YAML)
oak-dev status                                # clusters, nodes, instances
oak-dev shell worker -- ctr -n oakestra containers ls   # what actually ran
oak-dev logs mqtt --no-follow --tail 200      # the control-plane messages
```

`oak-dev doctor --fix` points the `oak` CLI at this stack, so `oak` commands
work without further configuration. The MQTT tap is the only way to watch
cluster<->worker messages like `nodes/<id>/control/deploy` - they never appear
in any container's stdout.

## Working on the shared Python libraries

Edits to `oakestra_utils_library` are invisible by default: the libraries are
pip-installed from the `versions.lib_branch` GitHub branch when the image is
built, so no reload can reach them.

```bash
oak-dev config set libs_repo ../oakestra_utils_library
oak-dev up                        # recreates live Python containers with the mount
```

The mount is generated only for the Python components already in `live:`
(`oak-dev status` shows the set), so add the ones you need first with
`oak-dev reload <component>`.

## Turning the upstream extras back on

This repo ships lean compose overrides: no dashboard, observability or addons.

```bash
oak-dev config set profiles.dashboard true
oak-dev up
```

`profiles.observability` and `profiles.addons` work the same way. Leave them
off unless the task needs them - each adds containers, image pulls and startup
time to every `up`.

## Partial stacks

`--stack worker` brings up cluster + worker only (the worker needs a cluster
to register against); `--stack root` or `--stack cluster` isolates one
orchestrator. The scope chosen by `up --stack ...` is sticky across later
commands (`.generated/stack`) until a plain `down` clears it - `oak-dev
status` always prints the active scope and where it came from.

## Single test file / single test

```bash
oak-dev test -- tests/test_03_deployment.py
oak-dev test -- -k test_worker_attached
```

Test files run in a fixed, meaningful order: `test_01_health` ->
`test_02_registration` -> `test_03_deployment` -> `test_04_network` ->
`test_05_failures`. `test_03`/`test_04` share module-scoped fixtures and
intentionally mutate shared state in sequence (deploy -> scale ->
undeploy/delete), so running a narrow `-k` filter against them can behave
differently than running the file as a whole.

## Debugging

```bash
oak-dev debug cm                  # attach a debugger from the CLI
oak-dev vscode install            # generate .vscode/{launch,tasks}.json instead
```

Recreates the component with Delve (Go) or debugpy (Python) in front of it.
`oak-dev vscode install` generates the matching launch.json/tasks.json from
the component registry, so F5 on "oak-dev: attach <component>" does the same
thing without a terminal - its `preLaunchTask` runs `oak-dev debug
<component> --wait` first. Returning to normal: `oak-dev reload <component>`
detaches most components, but anything living in the worker container
(nodeengine, netmanager) needs `oak-dev up --stack worker` instead - `reload`
deliberately never recreates the worker (see troubleshooting.md).

## Debugging the running system

```bash
oak-dev status                                          # scope, live set, clusters, nodes, containers, URLs
oak-dev logs [target...]                                # stack | component | container | mqtt
oak-dev shell worker -- ctr -n oakestra containers ls    # deployed workloads
oak-dev shell mongo-root                                 # mongosh
```

## Resetting state

```bash
oak-dev down --volumes    # wipe volumes, fresh start (confirmation prompt)
oak-dev reset             # drop app DBs + restart services, no image work (~15s)
```

Prefer `reset` for "the state is weird" - stuck jobs, odd scheduler state -
over a full `down`/`up` cycle. It keeps images and the worker's node ID.
