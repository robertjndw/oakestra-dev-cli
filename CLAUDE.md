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
- `override-cluster-servicemanager.yml`: upstream wires `ROOT_SERVICE_MANAGER_URL=${SYSTEM_MANAGER_URL}`, correct for IP-based multi-machine setups but wrong on a shared container network ('system_manager' resolves to the wrong container, which does not serve port 10099). Without this, worker subnet requests silently fail and NodeEngine crashloops on NetManager registration.

### Worker container internals (`worker/docker-entrypoint.sh`)

Startup order is load-bearing:

1. Wait for cluster_manager TCP (no cross-project `depends_on` exists).
2. Remove stale pid/socket files - after a nodeengined crash the container restarts with the old writable layer, and a leftover containerd socket makes existence checks pass before containerd listens.
3. Start standalone containerd and wait until `ctr version` answers. NodeEngine dials `/run/containerd/containerd.sock` exactly once at startup; if that dial fails it keeps a nil client and later panics on the first deploy (upstream bug). A socket-file check is NOT sufficient.
4. `socat` forwards cluster port 10100 and MQTT 10003 to localhost - NodeEngine uses a single cluster address for both, but they are separate containers.
5. Start NetManager, wait for its unix socket, then `exec nodeengined`.

`/var/lib/containerd` is an anonymous volume (see `compose/worker.yml`): containerd overlayfs snapshots cannot be created on the container's own overlayfs (rootfs mounts fail with `invalid argument`). Workloads run in containerd namespace `oakestra`.

### Test suite (`tests/`)

- Files run in order: `test_01_health` → `test_02_registration` → `test_03_deployment`. The deployment tests share one module-scoped app fixture and intentionally mutate shared state in sequence (deploy → scale up → scale down → undeploy/delete).
- `wait_until` (helpers.py) retries all exceptions EXCEPT `AssertionError`, which propagates immediately - that is the fail-fast contract used by `assert_not_failed` to abort polling when a job hits a terminal status. Keep that distinction when adding checks.
- `json_body` unwraps double-encoded responses: several system_manager endpoints return `json_util.dumps(...)` through flask-smorest, producing a JSON string containing JSON. Use it for any new API call.
- `ApiClient` re-logs-in on 401 (JWT expires after ~15 min).
- Config via env (see `.env.example`): `OAK_ROOT_API`, `OAK_READY_TIMEOUT` (boot/registration waits), `OAK_DEPLOY_TIMEOUT` (RUNNING waits, includes image pull).

## Gotchas

- **Never restart/recreate the worker while deployment tests run.** Each new worker container registers a new node ID; instances scheduled to the old ID stay `NODE_SCHEDULED` forever and the tests time out. Stale node candidates from previous worker containers linger in the cluster DB until they age out.
- `NETMANAGER_VERSION` defaults to `alpha-` + `$OAKESTRA_REPO/version.txt` - oakestra-net publishes develop builds only under `alpha-` tags; plain version tags may not exist.
- Local changes to `oakestra` Python services, scheduler, and NodeEngine are tested directly. Exceptions: oakestra-net comes from GHCR/releases (uncomment the `override-local-service-manager.yml` lines in the Makefile for local builds), and the shared Python libraries are installed from the `LIB_BRANCH` GitHub branch at image build time - local library edits are invisible until pushed.
- `make worker-down` uses `down -v` deliberately: anonymous containerd volumes would otherwise accumulate per recreation.
