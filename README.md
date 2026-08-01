# oakestra-macos-testing

Local dev + testing setup for [Oakestra](https://github.com/oakestra/oakestra) on macOS
(Linux-compatible - only the OrbStack VM path is Mac-only).

It runs the full Oakestra stack (root orchestrator, cluster orchestrator, and a
dockerized worker node) from your local `oakestra` checkout, lets you edit that
source live without rebuilding images, and verifies changes with an
end-to-end test suite: cluster registration, worker attachment, and a full
deploy / scale / undeploy lifecycle of a real container workload.

All of it - compose file assembly, live-mount overrides, cross-compiling,
debugger wiring, doctor checks, the watch loop - lives in the **`oak-dev`** Go
CLI (`cmd/oak-dev`), which `make install` puts on your PATH. `oak-dev --help`
is the source of truth. The `Makefile` covers only what oak-dev can't do for
itself: installing oak-dev, and driving the OrbStack VM. See `CLAUDE.md` for
why the compose overrides in `compose/` exist.

## Prerequisites

- Docker with Compose v2.18+ (Docker Desktop or [OrbStack](https://orbstack.dev), OrbStack recommended)
- Go 1.22+ (builds `oak-dev` itself, plus the cross-compiled scheduler/NodeEngine binaries)
- Python 3.10+ (for the pytest suite)
- A local checkout of `oakestra` (sibling directory `../oakestra` by default)
- Optional: [`watchexec`](https://watchexec.github.io) for `oak-dev dev`, the real `oak` CLI ([oakestra-cli](https://github.com/oakestra/oakestra-cli)) for `oak-dev status`, `orbctl` for the OrbStack VM path

`oak-dev doctor` checks all of the above and tells you exactly what's missing.

## Quickstart

```bash
make install                          # build oak-dev onto your PATH
cp .env.example .env                  # once, adjust paths/values if needed
cp oak-dev.yaml.example oak-dev.yaml  # once, pick your live: components + stack
oak-dev doctor --fix                  # prerequisites, venv, protobuf stubs, oak CLI
oak-dev dev                           # start everything, watch for edits, stream logs
```

The first run takes a few minutes (it builds all images from source). Later
runs are fast thanks to the Docker layer cache - and a service in
`oak-dev.yaml`'s `live:` list doesn't rebuild an image at all.

Everything is `oak-dev`. The `Makefile` only installs it and drives the
OrbStack VM (see [Real worker in an OrbStack VM](#real-worker-in-an-orbstack-vm)).

## The twelve commands

```
Start and stop            Work on the code          Look inside
  up                        dev                       logs
  down                      reload                    shell
  reset                     debug                     status
                            test
                                                    Set up
                                                      doctor
                                                      config
```

`oak-dev --help` groups them exactly like this, and every command has a
`--help` with real examples. Two conventions apply throughout:

**Components** can be written in full, by alias, or by any unambiguous prefix.
`cluster_manager`, `cm` and `cluster_man` are the same thing; `-` and `_` are
interchangeable. Ambiguity is an error that lists the candidates, never a
silent guess.

| Component | Alias | Language | Repo | Source |
|---|---|---|---|---|
| `system_manager` | `sm` | Python | oakestra | `root_orchestrator/system-manager-python` |
| `jwt_generator` | `jwt` | Python | oakestra | `root_orchestrator/jwt-generator` |
| `root_resource_abstractor` | `rra` | Python | oakestra | `resource-abstractor` |
| `cluster_manager` | `cm` | Python | oakestra | `cluster_orchestrator/cluster-manager` |
| `cluster_resource_abstractor` | `cra` | Python | oakestra | `resource-abstractor` |
| `scheduler` | `sched` | Go | oakestra | `scheduler` (runs in both root and cluster) |
| `nodeengine` | `ne` | Go | oakestra | `go_node_engine` (the worker) |
| `root_service_manager` | `rsm` | Python | oakestra-net | `root-service-manager/service-manager` |
| `cluster_service_manager` | `csm` | Python | oakestra-net | `cluster-service-manager/service-manager` |
| `netmanager` | `nm` | Go | oakestra-net | `node-net-manager` (the worker) |

Both resource abstractors share one source tree, so editing it affects both.
`scheduler` is one binary in two containers, so `oak-dev reload sched`
restarts both; narrow with `--stack root` when you mean one. The oakestra-net
components come from a separate checkout (`OAKESTRA_NET_REPO`, default
`../oakestra-net`) - `oak-dev doctor` checks for it, but only as an optional
check, since it's not needed unless you're editing one of those three.
`netmanager` shares the worker container with `nodeengine`; `oak-dev reload
netmanager` restarts nodeengined too, since NodeEngine only registers with
NetManager's socket once, at startup.

**Scope** is set by `--stack full|root|cluster|worker` on any command, and
defaults to `oak-dev.yaml`'s `stack:`. The scope you pass to `up` becomes
sticky - recorded in `.generated/stack` and reused by later commands, so you
don't repeat `--stack worker` all session. A plain `oak-dev down` clears it,
and `oak-dev status` always shows which scope is in effect and where it came
from. `worker` scope also starts `cluster`, since the worker registers
against it.

## Two walkthroughs

**Fixing how NodeEngine handles an MQTT deploy message:**

```bash
oak-dev dev ne --stack worker    # cluster + worker only, watching NodeEngine
# in another terminal:
oak application create -f fixtures/nginx.json
oak-dev logs mqtt                # watch nodes/<id>/control/deploy go past
# edit go_node_engine/... -> rebuild + in-place restart (~5s), same container
oak-dev debug ne                 # attach Delve when you need breakpoints
oak-dev test ne                  # go test ./..., no containers (~2s)
oak-dev test --smoke
```

**Fixing cluster_manager's node registration:**

```bash
oak-dev dev --stack cluster
# edit cluster_orchestrator/cluster-manager/... -> gunicorn --reload picks it up
oak-dev shell mongo-cluster      # mongosh, to inspect what actually landed
oak-dev debug cm                 # debugpy on :5681, breakpoint in a handler
oak-dev reset                    # ~15s: drop the databases instead of a full rebuild
oak-dev status                   # clusters registered, nodes attached, instances
```

## Command reference

### `up`, `down`, `reset`

```
oak-dev up [--stack S] [--workers N]
```
Builds if needed and starts every stack in scope, in root → cluster → worker
order (root and cluster declare the shared `oakestra` network; the worker
requires it to exist). `--workers N` runs N dockerized workers. Any live Go
component with no binary yet gets cross-compiled first, so its container never
starts against an empty `/oak-bin` mount.

```
oak-dev down [--stack S] [--volumes] [--yes]
```
Stops the stacks in scope, in reverse order. `--volumes` also deletes the
MongoDB, Redis and containerd volumes - a genuinely fresh start rather than a
restart - and prompts unless you pass `--yes`. A plain `oak-dev down` also
clears the sticky scope.

```
oak-dev reset [--yes]
```
The "state is weird" command, and much faster than `down --volumes && up`
(~15s, no image work). Drops every non-system database in the root and cluster
mongo instances, flushes both redis instances, and restarts the application
services so in-memory caches clear too. Admin login and cluster registration
come back cleanly because those services re-seed on boot. Does not touch the
NetManager databases (`mongo_rootnet`/`mongo_clusternet`), and does not
re-register the worker - it keeps its node ID until its next handshake.

### `dev`

```
oak-dev dev [component...] [--no-up] [--test smoke]
```
The one command to start working. Brings the stack up if it isn't running,
then merges the logs of every stack in scope into one prefixed stream and
watches the source of every live Go component, cross-compiling and restarting
it in place on each save. Live Python services need no watcher - their own
`gunicorn --reload` handles saves.

Naming components restricts the watchers to those (and mounts them from source
if they weren't already); logs still cover the whole scope. `--test smoke`
re-runs the smoke suite after each successful rebuild - never the full suite,
whose deployment tests are order-dependent and share state.

The worker is skipped automatically while `oak-dev test` holds its lock, since
restarting it mid-suite strands scheduled instances on a stale node ID.
Requires `watchexec`.

### `reload`

```
oak-dev reload [component...] [--image] [--no-live]
```
Makes what you just edited take effect. You don't pick the mechanism - the
component's language and configuration decide it:

| Component | What happens | Time |
|---|---|---|
| any Python service | already mounted under `gunicorn --reload`, nothing to do | instant |
| `scheduler` | cross-compile, restart the container in place | ~5s |
| `nodeengine`, `netmanager` | cross-compile, restart the process *inside* the worker | ~5s |
| anything, `--image` | `docker compose build` + recreate | ~30s |

Reach for `--image` when the change is one no bind-mount can pick up:
`requirements.txt`, a `Dockerfile`, `go.mod`. With no arguments, reloads
everything in `live:`.

A component that isn't in `live:` yet is added to `oak-dev.yaml` and recreated
from your working tree, so `live:` isn't something you have to set up in
advance. `--no-live` turns that back into an error. A failed build prints a red
`BUILD FAILED` banner and leaves the old binary running rather than silently
doing nothing.

`reload nodeengine`/`reload netmanager` restart their process, never the
container: a new container means a new hostname, which means `cluster_manager`
registers a new node ID and every instance scheduled to the old one is stuck
in `NODE_SCHEDULED` forever. `reload netmanager` also restarts `nodeengined`,
since NodeEngine only registers with NetManager's socket once, at startup.

### `debug`

```
oak-dev debug <component> [--stack S] [--no-live]
```
Recreates the container with a debugger in front of it - Delve for Go (built
with `-gcflags="all=-N -l"`, with Delve cross-compiled into
`build/linux_<arch>/dlv` on first use), debugpy for Python (`pip install`ed
into the running container) - and prints the `localhost` port to attach to.
Those ports match `.vscode/launch.json`. Mounts the component from source
first if it isn't already. `oak-dev reload <component>` puts it back.

```bash
oak-dev debug cm                  # debugpy on localhost:5681
oak-dev debug sched --stack root  # Delve on localhost:2345 (cluster's is 2346)
oak-dev debug ne                  # Delve on localhost:2347
```

### `test`

```
oak-dev test [component] [--smoke] [--no-up] [-- args...]
```
With no argument, runs the pytest end-to-end suite, starting the stack first
unless `--no-up`. `--smoke` runs health and registration only. With a
component, runs that component's own `go test ./...` on the host - no Docker,
no stack, ~2s. Anything after `--` goes straight through.

```bash
oak-dev test                              # the full suite
oak-dev test --smoke
oak-dev test sched -- -run TestBestFit
oak-dev test -- tests/test_03_deployment.py -k scale
```

Holds `.generated/test.lock` for the run, which is what tells `oak-dev dev`'s
watchers to leave the worker alone.

### `logs`, `shell`

```
oak-dev logs [target...] [--tail N] [--no-follow]
oak-dev shell <target> [-- cmd...]
```
Both take the same kind of target: a stack (`root`, `cluster`, `worker`), a
component by name/alias/prefix, a raw container name (`mongo_rootnet`,
`root_redis`, `cluster_service_manager` - anything in the compose files), or a
named endpoint. `oak-dev logs` with no target merges every stack in scope into
one colour-tagged stream.

The endpoints are the cases that want a specific client rather than a shell:

| Endpoint | What it does |
|---|---|
| `mqtt` | `mosquitto_sub -t '#'` on the broker - the only way to watch the cluster↔worker control plane live |
| `mongo-root` | `mongosh` against the root mongo (port 10007) |
| `mongo-cluster` | `mongosh` against the cluster mongo (port 10107) |

```bash
oak-dev logs                  # everything, merged
oak-dev logs cluster          # one stack
oak-dev logs sched            # both schedulers
oak-dev logs mqtt             # the control plane
oak-dev shell worker -- ctr -n oakestra containers ls
oak-dev shell mongo-root
```

`shell` prefers bash and falls back to sh for the alpine-based images.

### `status`

```
oak-dev status
```
Answers "is Oakestra healthy", not just "which containers are up". Starts with
the local setup - active scope and where it came from, which components run
from your working tree - then, if the real `oak` CLI is on PATH, the cluster
and instance state it reports, then a container table and the service URLs.

### `doctor`

```
oak-dev doctor [--fix]
```
Runs ten preflight checks and prints a fix for anything red. Checks marked
`(optional)` only gate one command and don't fail the run. `--fix` repairs what
it can instead of only describing it: creates the pytest venv, generates
`proto/*_pb2.py` into your oakestra checkout (needed before live-mounting
`system_manager`/`cluster_manager`, since those files are gitignored upstream
and normally only exist after an image build), and points the `oak` CLI at this
stack. The last two write outside this repo, so `--fix` names every file and
setting it touches.

### `config`

```
oak-dev config
oak-dev config get <key>
oak-dev config set <key> <value>
```
Reads or writes `oak-dev.yaml` settings without opening the file: `cluster.name`,
`cluster.location`, `workers`, `stack`, `oakestra_repo`, `libs_repo`,
`profiles.dashboard`/`observability`/`addons`, `versions.netmanager`,
`versions.lib_branch`. With no subcommand, prints every key's resolved value.
`set` edits `oak-dev.yaml` in place, keeping its comments, and warns if a
`.env`/environment variable (or, for `stack`, the sticky scope from an earlier
`up`/`down --stack`) currently outranks the value it just wrote. `live:` isn't
here - `reload`/`debug` already manage it.

### Shell completion

```bash
oak-dev completion install          # writes the script for $SHELL to the right place
oak-dev completion install fish     # or name one: bash, zsh, fish
oak-dev completion --help           # generate-only, for bash, zsh, fish, powershell
```
Completes commands, component names (showing each alias), stacks, endpoints
and live container names.

`install` saves finding the file yourself: fish gets
`~/.config/fish/completions/oak-dev.fish` (autoloaded, nothing else to do);
zsh and bash go to Homebrew's completion directories when `brew` is on PATH
(`$(brew --prefix)/share/zsh/site-functions/_oak-dev`,
`$(brew --prefix)/etc/bash_completion.d/oak-dev` - already wired into fpath/
bash-completion by Homebrew itself), otherwise `~/.zfunc/_oak-dev` or
`~/.local/share/bash-completion/completions/oak-dev`, printing the one-time rc
line those fallback paths need. It only ever writes that one script - never an
rc file. powershell isn't covered by `install`; use `oak-dev completion
powershell --help`.

## Moving from the old commands

| Was | Now |
|---|---|
| `oak-dev go <c>` | `oak-dev reload <c>` |
| `oak-dev rebuild <c>` | `oak-dev reload <c> --image` |
| `oak-dev worker reload` | `oak-dev reload ne` |
| `oak-dev worker scale N` | `oak-dev up --workers N` |
| `oak-dev unit <c>` | `oak-dev test <c>` |
| `oak-dev clean` | `oak-dev down --volumes` |
| `oak-dev protoc` | `oak-dev doctor --fix` |
| `oak-dev oak-config` | `oak-dev doctor --fix` |
| `oak-dev mongo root` | `oak-dev shell mongo-root` |
| `oak-dev mqtt-tap` | `oak-dev logs mqtt` |
| `oak-dev sh <container>` | `oak-dev shell <target>` |
| `make e2e` | `oak-dev test` |
| `make up` / `make down` / `make test` / ... | `oak-dev up` / `down` / `test` / ... |
| `make log s=X`, `make worker-logs` | `oak-dev logs X` |
| `make rebuild s=X` | `oak-dev reload X --image` |
| `make worker-shell` | `oak-dev shell worker` |
| `make open` | `oak-dev status` prints the URLs |
| `make vm-*` | unchanged |

## `oak-dev.yaml`

Copy `oak-dev.yaml.example`. Every key has a default, so an absent file still
works: full stack, nothing mounted from source. `.env` overrides the settings
both files cover (`OAKESTRA_REPO`, `OAKESTRA_NET_REPO`, `CLUSTER_NAME`,
`CLUSTER_LOCATION`, `LIB_BRANCH`, `NETMANAGER_VERSION`).

```yaml
oakestra_repo: ../oakestra
oakestra_net_repo: ../oakestra-net  # only for root_service_manager/cluster_service_manager/netmanager
libs_repo: null                # optional: local oakestra_utils_library checkout
cluster: {name: test-cluster, location: "52.5200,13.4050,100"}
workers: 1
live: [system_manager, cluster_manager]   # run these from your working tree
stack: full                     # full | root | cluster | worker
profiles: {dashboard: false, observability: false, addons: false}
versions: {netmanager: auto, lib_branch: develop}
```

`live:` and `stack:` are the two highest-leverage keys: services you aren't
editing are never built, and services you don't need are never started. You
rarely edit `live:` by hand, though - `oak-dev reload` and `oak-dev debug`
add what they need, and `oak-dev status` shows the current set. An unknown
name in either key is a startup error, not a silent no-op.

`oak-dev` writes `.generated/compose-args` (the effective `-f` chain per
stack) and `.generated/dev-live.yml` (the merged live overlay actually in
effect) so the result is always inspectable - safe to delete, regenerated on
every command.

## What the test suite covers

| File | What it verifies |
|---|---|
| `tests/test_01_health.py` | system_manager and cluster_manager answer HTTP, Swagger docs are served, Admin login returns a JWT |
| `tests/test_02_registration.py` | the cluster registers at the root and turns active, the worker attaches, aggregated cpu/memory resources reach the root |
| `tests/test_03_deployment.py` | SLA registration, instance deployment to `RUNNING`, scale up to 2 instances, scale down to 1, undeploy, application deletion |
| `tests/test_04_network.py` | overlay data plane: a one-shot client container wgets an nginx service via its round-robin service IP (10.30.0.0/16) and must exit 0 (`COMPLETED`) |
| `tests/test_05_failures.py` | negative paths: a 10TB-memory request is rejected with a capacity status, an unpullable image surfaces as `FAILED` with a status detail |

The deployment tests deploy a real nginx container inside the dockerized
worker (via the worker's own containerd), so a green run means the entire
chain worked: REST API -> root scheduler -> cluster scheduler -> MQTT ->
NodeEngine -> containerd.

Tests poll with generous timeouts (configurable via `OAK_READY_TIMEOUT` and
`OAK_DEPLOY_TIMEOUT` in `.env`) and fail fast when a job enters a terminal
failure state such as `NoActiveClusterWithCapacity` or `FAILED`.

**Never restart/recreate the worker while deployment tests run** - see
CLAUDE.md. `oak-dev dev`'s watchers skip the worker automatically while
`oak-dev test` holds its lock, and `oak-dev reload nodeengine` restarts the
daemon rather than the container for the same reason.

## Repository layout

```
├── Makefile                    # `make install`, plus the vm-* targets
├── cmd/oak-dev/                # the CLI's cobra commands, one file per command
├── internal/                   # components registry, config, topology, target, doctor, build, ...
├── compose/worker.yml          # dockerized worker (own compose project, shared network)
├── compose/override-*.yml      # macOS-specific fixes + live/debug overlays
├── worker/                     # DinD worker image: NodeEngine + NetManager + entrypoint
├── fixtures/                   # ready-to-deploy SLA JSON for `oak application create -f`
├── .vscode/launch.json         # debugger attach configs per component
├── tests/                      # pytest E2E suite
├── oak-dev.yaml.example        # config template (copy to oak-dev.yaml)
├── .env.example                # env config template (copy to .env)
└── pytest.ini
```

The orchestrator services themselves are NOT in this repo - they are built and
started from your local `oakestra` checkout (`OAKESTRA_REPO`). The worker
image builds NodeEngine from that same checkout via a Docker build context, so
whatever you have on disk is what gets tested.

## How it works

- The root and cluster stacks are started from the compose files in
  `$OAKESTRA_REPO`, with the lean overrides applied (no addons, no
  observability, no dashboard by default - see `profiles:` in `oak-dev.yaml`
  to add them back) to keep startup fast and resource usage low.
- `compose/override-*-mongo.yml` pin MongoDB to 8.2: the 8.0 images used
  upstream refuse to start on Linux kernel 6.19+
  ([SERVER-121912](https://jira.mongodb.org/browse/SERVER-121912)), which is
  what OrbStack and recent Docker Desktop VMs ship.
- `compose/override-*-servicemanager.yml` adapt the oakestra-net components to
  the shared-network topology: the cluster service manager runs inside
  cluster_manager's network namespace (upstream authorizes its calls to the
  root by source IP, assuming both cluster services share one host IP), it
  points at `root_service_manager` directly instead of the host IP, and both
  service manager images are pinned to the same oakestra-net release as the
  worker's NetManager binary.
- `compose/override-live-*.yml` bind-mount host source over the
  baked copy - Python services with `gunicorn --reload`, Go services by
  swapping in a cross-compiled binary via a directory mount (never a file
  mount: `go build` writes a new inode each time). `compose/override-debug-*.yml`
  layer a debugger on top (debugpy for Python, Delve for Go).
- All compose projects join one shared Docker network named `oakestra`, so
  containers resolve each other by name (`system_manager`, `cluster_manager`,
  `mqtt`) - no IP configuration needed.
- The worker is a privileged container running NodeEngine, NetManager, and its
  own containerd. Workloads deployed by Oakestra run inside that containerd
  (namespace `oakestra`). `socat` forwards the cluster and MQTT ports to
  localhost inside the container because NodeEngine expects both on a single
  address. `docker-entrypoint.sh` runs nodeengined in a supervised restart
  loop rather than `exec`ing it, so `oak-dev reload nodeengine` can restart it
  in place without recreating the container (recreating mints a new node ID and
  strands scheduled instances in `NODE_SCHEDULED` forever).

## Real worker in an OrbStack VM

The dockerized worker covers most testing. For cases that need a real
systemd-managed NodeEngine (installer changes, host-level behavior), use a
lightweight OrbStack Linux VM. macOS-only, and the one thing that stays in the
`Makefile` rather than moving into `oak-dev`. The VM reaches the Mac at
`host.orb.internal`, so no IP configuration is needed.

```bash
make vm-create        # one-time: create VM + install released NodeEngine (alpha)
make vm-up            # start NodeEngine
make vm-logs          # tail logs
make vm-down          # stop NodeEngine
make vm-shell         # shell into the VM
make vm-delete        # remove the VM entirely
```

To test local `go_node_engine` changes in the VM instead:

```bash
make vm-create-local  # one-time: VM + containerd + binaries built from source
make vm-up
# edit go_node_engine/... then:
make vm-rebuild       # recompile, push binary, restart (~10-15s per cycle)
```

Binaries are cross-compiled on the Mac for the VM's architecture and pushed
with `orbctl push` - no Go toolchain needed inside the VM. Use a different VM
name with `make vm-create WORKER_VM=my-vm`.

## Manual smoke testing

With the stack up:

```bash
# Obtain a JWT
TOKEN=$(curl -s -X POST http://localhost:10000/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"Admin","password":"Admin"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")

# Registered clusters
curl -s http://localhost:10000/api/clusters/active | python3 -m json.tool

# Cluster resources as seen by the root
curl -s http://localhost:11011/api/v1/resources/ | python3 -m json.tool

# Workloads running inside the worker's containerd
oak-dev shell worker -- ctr -n oakestra containers ls
```

Swagger UI: http://localhost:10000/api/docs

## Configuration reference

`oak-dev.yaml` (see above) and `.env` (copy from `.env.example`, authoritative
for the settings it covers):

| Variable | Default | Purpose |
|---|---|---|
| `OAKESTRA_REPO` | `../oakestra` | Path to the oakestra checkout to test |
| `OAKESTRA_NET_REPO` | `../oakestra-net` | Path to the oakestra-net checkout, for `root_service_manager`/`cluster_service_manager`/`netmanager` |
| `LIB_BRANCH` | `develop` | Branch of the shared Python libraries used in image builds |
| `NETMANAGER_VERSION` | `alpha-` + contents of `version.txt` | NetManager release baked into the worker (develop maps to alpha tags) |
| `CLUSTER_NAME` | `test-cluster` | Cluster name registered at the root |
| `CLUSTER_LOCATION` | Berlin coords | lat,lon,altitude of the cluster |
| `OAK_ROOT_API` | `http://localhost:10000` | Root API URL used by the tests |
| `OAK_CLUSTER_API` | `http://localhost:10100` | Cluster manager URL used by the tests |
| `OAK_READY_TIMEOUT` | `180` | Seconds to wait for boot/registration |
| `OAK_DEPLOY_TIMEOUT` | `300` | Seconds to wait for instances to reach RUNNING |

## Explicitly out of scope

- **Multi-cluster** - collides on Oakestra's hardcoded ports and `networks: name: oakestra`.
- **Tilt / Skaffold / DevSpace** - K8s-specific; `oak-dev` takes the ideas (live-update, aggregated watch+logs, partial stacks, doctor), not the dependency.
- **A local registry mirror** - solves image *distribution* across machines; compose builds land on the same daemon here.
- **A bespoke API client** - `oak-dev status` shells out to the real `oak` CLI instead, and `oak-dev doctor --fix` configures it.
