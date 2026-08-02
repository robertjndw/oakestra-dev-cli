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
itself: installing oak-dev, and driving the OrbStack VM. See
[CONTRIBUTION.md](CONTRIBUTION.md) for how it fits together internally.

## Prerequisites

- Docker with Compose v2.18+ (Docker Desktop or [OrbStack](https://orbstack.dev), OrbStack recommended)
- Go 1.22+ (builds `oak-dev` itself, plus the cross-compiled scheduler/NodeEngine binaries)
- Python 3.10+ (for the pytest suite)
- A local checkout of `oakestra` (sibling directory `../oakestra` by default)
- Optional: the real `oak` CLI ([oakestra-cli](https://github.com/oakestra/oakestra-cli)) for `oak-dev status`, `orbctl` for the OrbStack VM path

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

### A typical session

Say you're fixing how NodeEngine handles an MQTT deploy message:

```bash
oak-dev dev ne --stack worker      # cluster + worker only, watching NodeEngine
```

In another terminal:

```bash
oak application create -f fixtures/nginx.json   # deploy a real workload
oak-dev logs mqtt                               # watch nodes/<id>/control/deploy go past
# edit go_node_engine/... -> cross-compiled and restarted in place (~5s), same container
oak-dev debug ne                                # attach Delve when you need breakpoints
oak-dev test ne                                 # go test ./..., no containers (~2s)
oak-dev status                                  # clusters, nodes, instances, URLs
oak-dev down                                    # stop everything
```

Everything goes through `oak-dev`; the `Makefile` only installs it and drives
the OrbStack VM (see
[Real worker in an OrbStack VM (macOS only)](#real-worker-in-an-orbstack-vm-macos-only)).

## Configuration reference

Two files control settings, and `oak-dev config` reads/writes the first
without you opening it:

- **`oak-dev.yaml`** (copy `oak-dev.yaml.example`) - every key has a default,
  so an absent file still works: full stack, nothing mounted from source.
- **`.env`** (copy `.env.example`) - authoritative for the settings it
  covers, and overrides the matching `oak-dev.yaml`/default values.

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

| `.env` variable | Default | Purpose |
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

## Command reference

Every command below also accepts the global `--stack full|root|cluster|worker`
flag described under **Scope** in [The twelve commands](#the-twelve-commands)
- it's omitted from the signatures here since it applies uniformly, not just
to the commands where scoping is the main point.

| Command | What it does |
|---|---|
| `oak-dev up [--workers N]` | Builds if needed and starts every stack in scope, root → cluster → worker order. `--workers N` runs N dockerized workers. |
| `oak-dev down [--volumes] [--yes]` | Stops the stacks in scope, in reverse order. `--volumes` also deletes the MongoDB, Redis and containerd volumes for a genuinely fresh start (prompts unless `--yes`). Clears the sticky scope. |
| `oak-dev reset [--yes]` | The "state is weird" fix (~15s, no image work): drops every non-system database in root and cluster, flushes both redis instances, and restarts services so in-memory caches clear too. Doesn't re-register the worker. |
| `oak-dev dev [component...] [--no-up] [--test smoke]` | The one command to start working: brings the stack up, merges logs from every stack in scope, and cross-compiles/restarts live Go components on save. Python services live-reload via `gunicorn --reload` already. `--test smoke` reruns the smoke suite after each rebuild. |
| `oak-dev reload [component...] [--image] [--no-live]` | Makes an edit take effect - the mechanism depends on the component's language (see below). `--image` forces a full rebuild + recreate. Adds the component to `live:` automatically if it wasn't there; `--no-live` turns that into an error instead. |
| `oak-dev debug <component> [--no-live]` | Recreates the container with a debugger attached (Delve for Go, debugpy for Python) and prints the `localhost` port to attach to (matches `.vscode/launch.json`). |
| `oak-dev test [component] [--smoke] [--no-up] [-- args...]` | With no argument, runs the full pytest E2E suite (starts the stack unless `--no-up`). `--smoke` runs health + registration only. With a component, runs its own `go test ./...` on the host - no Docker. Anything after `--` passes straight through. |
| `oak-dev logs [target...] [--tail N] [--no-follow]` | Streams logs for a stack, component, container, or endpoint (see below). No target merges every stack in scope into one color-tagged stream. |
| `oak-dev shell <target> [-- cmd...]` | Opens a shell (or runs a command) in a stack, component, container, or endpoint. Prefers bash, falls back to sh. |
| `oak-dev status` | Answers "is Oakestra healthy" - active scope, which components run from your working tree, cluster/instance state (via the real `oak` CLI if present), a container table, and service URLs. |
| `oak-dev doctor [--fix]` | Runs preflight checks and prints a fix for anything red. `--fix` repairs what it can: creates the pytest venv, generates protobuf stubs, points the `oak` CLI at this stack. |
| `oak-dev config`, `oak-dev config get <key>`, `oak-dev config set <key> <value>` | Reads or writes `oak-dev.yaml` settings without opening the file. `set` edits the file in place, keeping comments, and warns if `.env` or the sticky scope outranks what it just wrote. |
| `oak-dev completion install [shell]` | Installs shell completion (bash/zsh/fish) to the right place for your shell. `completion --help` generates a script only (also covers powershell). |

**Reload mechanism by component:**

| Component | What happens | Time |
|---|---|---|
| any Python service | already mounted under `gunicorn --reload`, nothing to do | instant |
| `scheduler` | cross-compile, restart the container in place | ~5s |
| `nodeengine`, `netmanager` | cross-compile, restart the process *inside* the worker | ~5s |
| anything, `--image` | `docker compose build` + recreate | ~30s |

`reload nodeengine`/`reload netmanager` restart their process, never the
container: a new container means a new hostname, which means
`cluster_manager` registers a new node ID and every instance scheduled to the
old one is stuck in `NODE_SCHEDULED` forever. `reload netmanager` also
restarts `nodeengined`, since NodeEngine only registers with NetManager's
socket once, at startup.

**`logs`/`shell` endpoints** (in addition to a stack, component, or raw
container name):

| Endpoint | What it does |
|---|---|
| `mqtt` | `mosquitto_sub -t '#'` on the broker - the only way to watch the cluster↔worker control plane live |
| `mongo-root` | `mongosh` against the root mongo (port 10007) |
| `mongo-cluster` | `mongosh` against the cluster mongo (port 10107) |

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

**Never restart/recreate the worker while deployment tests run**: each new
worker container registers a new node ID, so instances scheduled to the old
one get stuck and the run times out. `oak-dev dev`'s watchers skip the worker
automatically while `oak-dev test` holds its lock, and `oak-dev reload
nodeengine` restarts the daemon rather than the container for the same
reason.

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

## Real worker in an OrbStack VM (macOS only)

The dockerized worker covers most testing. For cases that need a real
systemd-managed NodeEngine (installer changes, host-level behavior), use a
lightweight OrbStack Linux VM instead - the one thing that stays in the
`Makefile` rather than moving into `oak-dev`. The VM reaches the Mac at
`host.orb.internal`, so no IP configuration is needed.

```bash
make vm-create   # one-time: create VM + install released NodeEngine (alpha)
make vm-up       # start NodeEngine
make vm-logs     # tail logs
make vm-down     # stop NodeEngine
make vm-shell    # shell into the VM
make vm-delete   # remove the VM entirely
```

To test local `go_node_engine` changes in the VM instead, use
`make vm-create-local` (VM + containerd + binaries built from source), then
`make vm-rebuild` after each edit (~10-15s per cycle) - binaries are
cross-compiled on the Mac and pushed with `orbctl push`, no Go toolchain
needed inside the VM. Use a different VM name with `make vm-create
WORKER_VM=my-vm`.

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
