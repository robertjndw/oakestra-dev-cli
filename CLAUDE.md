# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Repo Is

E2E testing harness for [Oakestra](https://github.com/oakestra/oakestra) on macOS. The orchestrator code is NOT here - it is built from a local `oakestra` checkout pointed to by `OAKESTRA_REPO` (default `../oakestra`, configured in `.env`). This repo contains only the glue: a Makefile driving three Docker Compose projects, a dockerized worker image, macOS-specific compose overrides, and a pytest E2E suite.

## Commands

```bash
cp .env.example .env        # once
make e2e                    # start everything + run the full suite
make up / make down         # start/stop root + cluster + worker
make test                   # full suite against a running stack
make test-smoke             # health + registration only (skips deployment tests)
make help                   # all targets

# Single test file / single test
.venv/bin/pytest tests/test_03_deployment.py -v
.venv/bin/pytest tests/ -v -k test_worker_attached

# Iteration loops after editing code in $OAKESTRA_REPO
make rebuild s=system_manager        # root Python service (~15s)
make rebuild-cluster s=cluster_manager
make rebuild-scheduler               # shared Go scheduler, both stacks
make worker-up                       # rebuilds NodeEngine image from local source

# Debugging
make status
make log s=<container>
make worker-logs / make worker-shell
docker compose -f compose/worker.yml exec worker ctr -n oakestra containers ls  # deployed workloads
make clean                           # wipe volumes, fresh start (confirm prompt)
```

The venv lives at `.venv` (repo root) and is created automatically by `make test` via the `venv` target.

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

### Worker container internals (`worker/docker-entrypoint.sh`)

Startup order is load-bearing:

1. Wait for cluster_manager TCP (no cross-project `depends_on` exists).
2. Remove stale pid/socket files - after a nodeengined crash the container restarts with the old writable layer, and a leftover containerd socket makes existence checks pass before containerd listens.
3. Start standalone containerd and wait until `ctr version` answers. NodeEngine dials `/run/containerd/containerd.sock` exactly once at startup; if that dial fails it keeps a nil client and later panics on the first deploy (upstream bug). A socket-file check is NOT sufficient.
4. `socat` forwards cluster port 10100 and MQTT 10003 to localhost - NodeEngine uses a single cluster address for both, but they are separate containers.
5. Start NetManager, wait for its unix socket, then `exec nodeengined`.

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
- Local changes to `oakestra` Python services, scheduler, and NodeEngine are tested directly. Exceptions: oakestra-net comes from GHCR/releases (uncomment the `override-local-service-manager.yml` lines in the Makefile for local builds), and the shared Python libraries are installed from the `LIB_BRANCH` GitHub branch at image build time - local library edits are invisible until pushed.
- `make worker-down` uses `down -v` deliberately: anonymous containerd volumes would otherwise accumulate per recreation.
