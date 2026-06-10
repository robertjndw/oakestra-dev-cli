# oakestra-macos-testing

Local testing setup for [Oakestra](https://github.com/oakestra/oakestra) on macOS.

It runs the full Oakestra stack (root orchestrator, cluster orchestrator, and a
dockerized worker node) from your local `oakestra` checkout and verifies it with
an end-to-end test suite: cluster registration, worker attachment, and a full
deploy / scale / undeploy lifecycle of a real container workload.

Typical use: implement a feature in the `oakestra` repo, then run `make e2e`
here to prove the whole platform still works.

## Prerequisites

- Docker with Compose v2.17+ (Docker Desktop or [OrbStack](https://orbstack.dev), OrbStack recommended)
- Python 3.10+
- A local checkout of `oakestra` (sibling directory `../oakestra` by default)
- Optional: OrbStack CLI (`orbctl`) if you want a real Linux VM worker instead of the dockerized one

## Quickstart

```bash
cp .env.example .env   # once - adjust paths/values if needed
make e2e               # build + start everything + run the test suite
```

The first run takes a few minutes (builds all images from source). Subsequent
runs are fast thanks to the Docker layer cache.

When the stack is already running, iterate with just the tests:

```bash
make test          # full suite
make test-smoke    # health + registration only, skips deployments
```

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

## Repository layout

```
├── Makefile               # all entry points: make help
├── compose/worker.yml     # dockerized worker (own compose project, shared network)
├── compose/override-*.yml # macOS-specific fixes applied to the upstream stacks
├── worker/                # DinD worker image: NodeEngine + NetManager + entrypoint
├── tests/                 # pytest E2E suite
├── .env.example           # configuration template (copy to .env)
└── pytest.ini
```

The orchestrator services themselves are NOT in this repo - they are built and
started from your local `oakestra` checkout (`OAKESTRA_REPO` in `.env`). The
worker image builds NodeEngine from that same checkout via a Docker build
context, so whatever you have on disk is what gets tested.

## How it works

- The root and cluster stacks are started from the compose files in
  `$OAKESTRA_REPO`, with the lean overrides applied (no addons, no
  observability, no dashboard) to keep startup fast and resource usage low.
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
- All compose projects join one shared Docker network named `oakestra`, so
  containers resolve each other by name (`system_manager`, `cluster_manager`,
  `mqtt`) - no IP configuration needed.
- The worker is a privileged container running NodeEngine, NetManager, and its
  own containerd. Workloads deployed by Oakestra run inside that containerd
  (namespace `oakestra`). `socat` forwards the cluster and MQTT ports to
  localhost inside the container because NodeEngine expects both on a single
  address.

## Daily workflow

| Goal | Command |
|---|---|
| Start everything | `make up` |
| Run the E2E suite | `make test` |
| Stop everything | `make down` |
| Start/stop one stack | `make up-root` / `make down-cluster` |
| Show running containers | `make status` |
| Tail all root / cluster logs | `make logs-root` / `make logs-cluster` |
| Follow one container | `make log s=system_manager` |
| Rebuild after a change | `make rebuild s=system_manager` |
| Rebuild a cluster service | `make rebuild-cluster s=cluster_manager` |
| Rebuild the shared Go scheduler (both stacks) | `make rebuild-scheduler` |
| Restart without rebuild | `make restart s=system_manager` |
| Worker logs / shell | `make worker-logs` / `make worker-shell` |
| Simulate multiple workers | `make worker-scale n=3` |
| Open the API docs | `make open` |
| Wipe volumes (fresh start) | `make clean` |

`make help` lists everything.

## Iterating on Oakestra code

### Python services (system_manager, cluster_manager, resource abstractors)

The services run under gunicorn, so each change needs a rebuild. The pip layer
is cached, so this takes ~10-15 seconds:

```bash
# edit $OAKESTRA_REPO/root_orchestrator/system-manager-python/...
make rebuild s=system_manager
make log s=system_manager
make test-smoke            # or make test for the full lifecycle
```

### Scheduler (Go, shared between root and cluster)

```bash
# edit $OAKESTRA_REPO/scheduler/...
make rebuild-scheduler
make test
```

### NodeEngine (go_node_engine)

The worker image builds NodeEngine from your local source, so:

```bash
# edit $OAKESTRA_REPO/go_node_engine/...
make worker-up             # rebuilds the image (cached Go layers) and restarts
make worker-logs
make test
```

### oakestra-net

By default the network services (`root_service_manager`,
`cluster_service_manager`) use pre-built GHCR images, and the worker downloads
the NetManager release matching `$OAKESTRA_REPO/version.txt` (override with
`NETMANAGER_VERSION` in `.env`). To build the service managers from a local
`oakestra-net` checkout, uncomment the `override-local-service-manager.yml`
lines in the `Makefile`.

### Shared Python libraries

If your branch changes `oakestra_utils_library` or `resource_abstractor_client`,
set `LIB_BRANCH` in `.env` to your branch name - otherwise images build against
the `develop` versions of the libraries.

## Real worker in an OrbStack VM

The dockerized worker covers most testing. For cases that need a real
systemd-managed NodeEngine (installer changes, host-level behavior), use a
lightweight OrbStack Linux VM. The VM reaches the Mac at `host.orb.internal`,
so no IP configuration is needed.

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
with `orbctl push` - no Go toolchain needed inside the VM.

Use a different VM name with `make vm-create WORKER_VM=my-vm`.

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
docker compose -f compose/worker.yml exec worker ctr -n oakestra containers ls
```

Swagger UI: http://localhost:10000/api/docs

## Configuration reference

All settings live in `.env` (copy from `.env.example`):

| Variable | Default | Purpose |
|---|---|---|
| `OAKESTRA_REPO` | `../oakestra` | Path to the oakestra checkout to test |
| `LIB_BRANCH` | `develop` | Branch of the shared Python libraries used in image builds |
| `NETMANAGER_VERSION` | `alpha-` + contents of `version.txt` | NetManager release baked into the worker (develop maps to alpha tags) |
| `CLUSTER_NAME` | `test-cluster` | Cluster name registered at the root |
| `CLUSTER_LOCATION` | Berlin coords | lat,lon,altitude of the cluster |
| `OAK_ROOT_API` | `http://localhost:10000` | Root API URL used by the tests |
| `OAK_CLUSTER_API` | `http://localhost:10100` | Cluster manager URL used by the tests |
| `OAK_READY_TIMEOUT` | `180` | Seconds to wait for boot/registration |
| `OAK_DEPLOY_TIMEOUT` | `300` | Seconds to wait for instances to reach RUNNING |
