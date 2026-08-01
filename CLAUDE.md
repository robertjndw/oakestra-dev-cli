# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Repo Is

E2E testing harness for [Oakestra](https://github.com/oakestra/oakestra) on macOS. The orchestrator code is NOT here - it is built from a local `oakestra` checkout pointed to by `OAKESTRA_REPO` (default `../oakestra`, configured in `.env`/`oak-dev.yaml`). This repo contains only the glue: the `oak-dev` Go CLI (`cmd/oak-dev`, `internal/`) driving three Docker Compose projects, a dockerized worker image, macOS-specific compose overrides, and a pytest E2E suite. `oak-dev --help` (or README.md) is the source of truth for what it can do: partial stacks, source mounted from your working tree (no image rebuild for Python/Go edits), cross-compiling, debugger attach, and a watch+aggregated-logs `dev` command. The `Makefile` covers only `make install` and the `vm-*` OrbStack targets.

## Commands

```bash
make install                         # build oak-dev onto your PATH (PREFIX=~/.local/bin)
cp .env.example .env                 # once
cp oak-dev.yaml.example oak-dev.yaml # once - pick live: components + stack
oak-dev doctor --fix                 # preflight + venv + protobuf stubs + oak CLI config
oak-dev dev                          # start everything, watch for edits, stream logs
oak-dev up / oak-dev down            # start/stop (--stack worker for a subset)
oak-dev test                         # full suite (starts the stack if needed)
oak-dev test --smoke                 # health + registration only
oak-dev --help                       # all eleven commands, grouped

# Single test file / single test - pass through with --
oak-dev test -- tests/test_03_deployment.py
oak-dev test -- -k test_worker_attached

# Iteration loops after editing code in $OAKESTRA_REPO. Components take an
# alias or any unambiguous prefix: sm, jwt, rra, cm, cra, sched, ne.
oak-dev reload sched                 # cross-compile + in-place restart (~5s), both stacks
oak-dev reload ne                    # same for the worker; restarts nodeengined, not the container
oak-dev reload cm                    # Python: already live under gunicorn --reload, a no-op
oak-dev reload sm --image            # full image rebuild (~30s), for requirements.txt/Dockerfile
oak-dev test sched                   # go test ./..., no containers (~2s)
oak-dev debug cm                     # attach a debugger, see .vscode/launch.json
oak-dev dev sched                    # watch just the scheduler

# reload/debug add the component to oak-dev.yaml's live: themselves and
# recreate its container; --no-live turns that back into an error.

# Debugging
oak-dev status                       # scope, live set, clusters, nodes, containers, URLs
oak-dev logs [target...]             # stack | component | container | mqtt
oak-dev shell worker -- ctr -n oakestra containers ls   # deployed workloads
oak-dev shell mongo-root             # mongosh
oak-dev down --volumes               # wipe volumes, fresh start (confirm prompt)
oak-dev reset                        # drop DBs + restart services, no image work (~15s)
```

The venv lives at `.venv` (repo root) and is created automatically by `oak-dev doctor` or any `oak-dev test` run (`internal/testsuite.EnsureVenv`).

`--stack full|root|cluster|worker` is a persistent flag on every command and always means *scope*. The scope passed to `up` is sticky (`.generated/stack`) until the next plain `down`; `oak-dev status` prints which scope is active and where it came from.

## Architecture

Three separate compose projects join one shared Docker network named `oakestra`, so containers resolve each other by name across projects:

1. **Root orchestrator** - compose files from `$OAKESTRA_REPO/root_orchestrator/` with lean overrides (no addons/observability/dashboard) plus `compose/override-root-mongo.yml`.
2. **Cluster orchestrator** - same pattern, plus `compose/override-cluster-mongo.yml` and `compose/override-cluster-servicemanager.yml`.
3. **Worker** (`compose/worker.yml`, project name `oakestra-worker`) - a single privileged container built from `worker/Dockerfile`. NodeEngine is compiled from `$OAKESTRA_REPO/go_node_engine` via the `oakestra-src` additional build context; NetManager is downloaded from oakestra-net GitHub releases.

### Why the local overrides exist (do not remove casually)

- `override-*-mongo.yml`: upstream pins `mongo:8.0`, which refuses to start on Linux kernel 6.19+ (OrbStack ships newer). Pinned to 8.2 here.
- `override-cluster-servicemanager.yml` fixes two shared-network breakages:
  1. Upstream wires `ROOT_SERVICE_MANAGER_URL=${SYSTEM_MANAGER_URL}`, correct for IP-based multi-machine setups but wrong here ('system_manager' resolves to the wrong container, which does not serve port 10099). Without the fix, worker subnet requests silently fail and NodeEngine crashloops on NetManager registration.
  2. cluster_service_manager runs with `network_mode: "service:cluster_manager"` (one shared IP). The root net plugin authorizes the cluster's table-query calls by source IP, against the address captured from cluster_manager's gRPC handshake. With per-container IPs those calls get 400 'Invalid cluster address' and ALL overlay traffic silently times out while deployments still look RUNNING. Consequences of the shared netns: cluster_manager reaches the service manager via `CLUSTER_SERVICE_MANAGER_ADDR=localhost`, port 10110 is published on cluster_manager, and the upstream `cluster_manager -> cluster_service_manager` depends_on is inverted with `!override`.
- `override-*-servicemanager.yml` also pin both service manager images to `NETMANAGER_VERSION` instead of `:latest`, keeping all three oakestra-net components on the same release as the worker's NetManager binary.
- `override-live-*.yml`/`override-debug-*.yml` (`oak-dev up`/`go`/`debug`): bind-mount host Python source (gunicorn `--reload`) or a cross-compiled host Go binary (directory mount only - a file mount keeps resolving the old inode) over the baked image. Paths must be absolute (`${OAKESTRA_REPO}`/`${OAK_DEV_ROOT}`) - compose resolves relative paths against the *first* `-f` file's directory, not the invoking cwd.

### Worker container internals (`worker/docker-entrypoint.sh`)

Startup order is load-bearing:

1. Wait for cluster_manager TCP (no cross-project `depends_on` exists).
2. Remove stale pid/socket files - after a nodeengined crash the container restarts with the old writable layer, and a leftover containerd socket makes existence checks pass before containerd listens.
3. Start standalone containerd and wait until `ctr version` answers. NodeEngine dials `/run/containerd/containerd.sock` exactly once at startup; if that dial fails it keeps a nil client and later panics on the first deploy (upstream bug). A socket-file check is NOT sufficient.
4. `socat` forwards cluster port 10100 and MQTT 10003 to localhost - NodeEngine uses a single cluster address for both, but they are separate containers.
5. Start NetManager, wait for its unix socket, then run `nodeengined` in a supervised restart loop (not `exec`'d) - `oak-dev reload nodeengine` sends it a `pkill nodeengined` and the loop relaunches it without recreating the container. `OAK_DEV_DEBUG=1` swaps in `dlv exec` in the same loop.

`/var/lib/containerd` is an anonymous volume (see `compose/worker.yml`): containerd overlayfs snapshots cannot be created on the container's own overlayfs (rootfs mounts fail with `invalid argument`). Workloads run in containerd namespace `oakestra`.

### Test suite (`tests/`)

- Files run in order: `test_01_health` → `test_02_registration` → `test_03_deployment` → `test_04_network` → `test_05_failures`. The deployment and network tests share module-scoped app fixtures and intentionally mutate shared state in sequence (deploy → scale → undeploy/delete; web before client).
- `test_04_network` proves the overlay data plane: it requests a fixed RR service IP in the SLA (`10.30.30.30`) and uses a `one_shot` busybox client whose exit code becomes the verdict (exit 0 → NodeEngine reports `COMPLETED`). `test_05_failures` expects failure statuses, so it deliberately does NOT use `assert_not_failed`.
- `wait_until` (helpers.py) retries all exceptions EXCEPT `AssertionError`, which propagates immediately - that is the fail-fast contract used by `assert_not_failed` to abort polling when a job hits a terminal status. Keep that distinction when adding checks.
- `json_body` unwraps double-encoded responses: several system_manager endpoints return `json_util.dumps(...)` through flask-smorest, producing a JSON string containing JSON. Use it for any new API call.
- `ApiClient` re-logs-in on 401 (JWT expires after ~15 min).
- Config via env (see `.env.example`): `OAK_ROOT_API`, `OAK_READY_TIMEOUT` (boot/registration waits), `OAK_DEPLOY_TIMEOUT` (RUNNING waits, includes image pull).

## Gotchas

- **Never restart/recreate the worker while deployment tests run.** Each new worker container registers a new node ID; instances scheduled to the old ID stay `NODE_SCHEDULED` forever and the tests time out. Stale node candidates from previous worker containers linger in the cluster DB until they age out.
- `NETMANAGER_VERSION` defaults to `alpha-` + `$OAKESTRA_REPO/version.txt` - oakestra-net publishes develop builds only under `alpha-` tags; plain version tags may not exist.
- Local changes to `oakestra` Python services, scheduler, and NodeEngine are tested directly. Exceptions: oakestra-net comes from GHCR/releases, and the shared Python libraries are installed from the `LIB_BRANCH` GitHub branch at image build time - local library edits are invisible until pushed.
- `oak-dev down --stack worker --volumes` is worth preferring over a plain `down` when recreating the worker: its anonymous containerd volumes otherwise accumulate one per recreation.
- `resource_abstractor.py` hardcodes `app.run(..., debug=False)` - `FLASK_DEBUG=TRUE` does nothing for it (unlike system_manager/cluster_manager, already on gunicorn). Live-reload needs wrapping in `gunicorn --reload` too.
- Worker node ID stability is decided by `cluster_manager`, not NodeEngine: `PUT /api/v1/resources` dedups registration by `candidate_name` (the reported hostname). ID reuse across a restart only holds as long as the *container* (hence hostname) isn't recreated.
- Compose `command:`/`environment:` overrides can't reference a sibling field like `${RESOURCE_ABSTRACTOR_PORT}` - `${VAR}` interpolates against the host shell env at `docker compose` invocation time, not the container's own `environment:` list. Hardcode the value instead.
- `docker info --format '{{.Architecture}}'` reports GNU arch names (`aarch64`/`x86_64`); Go/`uname -m` on Apple Silicon reports `arm64`. Normalize both before comparing.
- A stale exited container from an earlier session can reference a since-deleted Docker network ID, making `restart`/`up` fail with `network ... not found`. Not a code bug - `docker rm -f` the stale container(s) and retry.
- `oak` (oakestra-cli) SLA files are JSON, not YAML (`oak application create -f fixtures/nginx.json`). Config keys: `oak config set system_manager_ip/cluster_manager_ip/cluster_name/cluster_location`, `oak config credentials <user> <pass>`.
