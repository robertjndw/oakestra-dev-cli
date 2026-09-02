# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Repo Is

E2E testing harness for [Oakestra](https://github.com/oakestra/oakestra) on macOS. The orchestrator code is NOT here - it is built from a local `oakestra` checkout pointed to by `OAKESTRA_REPO` (default `../oakestra`, configured in `.env`/`oak-dev.yaml`). The overlay-networking components (root/cluster service managers, NetManager) come from a separate `oakestra-net` checkout, `OAKESTRA_NET_REPO` (default `../oakestra-net`) - optional, since those three components normally run from pinned GHCR images/release binaries. This repo contains only the glue: the `oak-dev` Go CLI (`cmd/oak-dev`, `internal/`) driving three Docker Compose projects, a dockerized worker image, macOS-specific compose overrides, and a Go E2E suite (`e2e/`, plus its untagged helper library `internal/oakapi`). `oak-dev --help` (or README.md) is the source of truth for what it can do: partial stacks, source mounted from your working tree (no image rebuild for Python/Go edits), cross-compiling, debugger attach, and a watch+aggregated-logs `dev` command. The `Makefile` covers only `make install` and the `vm-*` OrbStack targets.

This file (and the CLAUDE.md symlink to it) covers *changing this repo*. For
*driving the CLI* - the doc an agent needs while working in `../oakestra`,
`../oakestra-net`, or `oakestra-deploy` - see the portable skill at
[skills/oak-dev/](skills/oak-dev/SKILL.md), installable anywhere with
`oak-dev skill install`.

## Commands

```bash
make install                         # build oak-dev onto your PATH (PREFIX=~/.local/bin)
cp .env.example .env                 # once
cp oak-dev.yaml.example oak-dev.yaml # once - pick live: components + stack
oak-dev doctor --fix                 # preflight + protobuf stubs + oak CLI config
oak-dev dev                          # start everything, watch for edits, stream logs
oak-dev up / oak-dev down            # start/stop (--stack worker for a subset)
oak-dev test                         # full suite (starts the stack if needed)
oak-dev test --smoke                 # health + registration only
oak-dev --help                       # all fourteen commands, grouped
oak-dev config                       # print resolved settings; `config set <key> <value>` edits oak-dev.yaml

# Single test / single suite - pass through with --
oak-dev test -- -run TestDeploymentLifecycle
oak-dev test -- -run TestSmokeRegistration/worker_attached

# Iteration loops after editing code in $OAKESTRA_REPO ($OAKESTRA_NET_REPO for
# rsm/csm/nm). Components take an alias or any unambiguous prefix: sm, jwt,
# rra, cm, cra, sched, ne, rsm, csm, nm.
oak-dev reload sched                 # cross-compile + in-place restart (~5s), both stacks
oak-dev reload ne                    # same for the worker; restarts nodeengined, not the container
oak-dev reload cm                    # Python: already live under gunicorn --reload, a no-op
oak-dev reload sm --image            # full image rebuild (~30s), for requirements.txt/Dockerfile
oak-dev test sched                   # go test ./..., no containers (~2s)
oak-dev debug cm                     # attach a debugger, or F5 in VS Code - see oak-dev vscode install
oak-dev dev sched                    # watch just the scheduler
oak-dev vscode install               # generate .vscode/{launch,tasks}.json from the component registry

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

`--stack full|root|cluster|worker` is a persistent flag on every command and always means *scope*. The scope passed to `up` is sticky (`.generated/stack`) until the next plain `down`; `oak-dev status` prints which scope is active and where it came from.

## Developing oak-dev itself

`.github/workflows/ci.yml` runs on every PR, two jobs, both `ubuntu-latest`: a
`go` job (build, vet, `go test -tags e2e -run '^$' ./e2e/...` to compile-check
the tagged suite without a stack, gofmt, `go test ./... -race`) and a `lint`
job (`golangci-lint`, config in `.golangci.yml` - `build-tags: [e2e]` there is
what makes the linter see the tagged files at all). Neither job brings up a
stack or clones `oakestra`; there is no live-stack job in this repo's CI.
Match the `go` job locally before pushing:
```bash
go build ./... && go vet ./... && gofmt -l .
go test -tags e2e -run '^$' ./e2e/...
go vet -tags e2e ./...
go test ./... -race        # internal/watch and multilog are concurrent - always run with -race
golangci-lint run ./...
make install                # rebuild the installed binary to manually exercise a change
```
Tests are stdlib-only (no testify), white-box (`package x`, not `x_test`), table-driven, using `t.TempDir()`/`t.Setenv()`/`t.Helper()` - see `internal/config/config_test.go` and `internal/oakapi/*_test.go` for worked examples.

`skills/` is embedded into the binary via `embed.go`, so an edit to `SKILL.md` or `references/*.md` isn't visible to `oak-dev skill install`/`skill status` until `make install` rebuilds the binary those commands run from.

## Architecture

Three separate compose projects join one shared Docker network named `oakestra`, so containers resolve each other by name across projects:

1. **Root orchestrator** - compose files from `$OAKESTRA_REPO/root_orchestrator/` with lean overrides (no addons/observability/dashboard) plus `compose/override-root-mongo.yml`.
2. **Cluster orchestrator** - same pattern, plus `compose/override-cluster-mongo.yml` and `compose/override-cluster-servicemanager.yml`.
3. **Worker** (`compose/worker.yml`, project name `oakestra-worker`) - a single privileged container built from `worker/Dockerfile`. NodeEngine is compiled from `$OAKESTRA_REPO/go_node_engine` via the `oakestra-src` additional build context; NetManager is downloaded from oakestra-net GitHub releases by default, or built from `$OAKESTRA_NET_REPO/node-net-manager` (the `oakestra-net-src` context, `NETMANAGER_SOURCE=local` build arg) when `netmanager` is in `live:` - see `compose/override-live-netmanager.yml`.

### Why the local overrides exist (do not remove casually)

- `override-*-mongo.yml`: upstream pins `mongo:8.0`, which refuses to start on Linux kernel 6.19+ (OrbStack ships newer). Pinned to 8.2 here.
- `override-cluster-servicemanager.yml` fixes two shared-network breakages:
  1. Upstream wires `ROOT_SERVICE_MANAGER_URL=${SYSTEM_MANAGER_URL}`, correct for IP-based multi-machine setups but wrong here ('system_manager' resolves to the wrong container, which does not serve port 10099). Without the fix, worker subnet requests silently fail and NodeEngine crashloops on NetManager registration.
  2. cluster_service_manager runs with `network_mode: "service:cluster_manager"` (one shared IP). The root net plugin authorizes the cluster's table-query calls by source IP, against the address captured from cluster_manager's gRPC handshake. With per-container IPs those calls get 400 'Invalid cluster address' and ALL overlay traffic silently times out while deployments still look RUNNING. Consequences of the shared netns: cluster_manager reaches the service manager via `CLUSTER_SERVICE_MANAGER_ADDR=localhost`, port 10110 is published on cluster_manager, and the upstream `cluster_manager -> cluster_service_manager` depends_on is inverted with `!override`.
- `override-*-servicemanager.yml` also pin both service manager images to `NETMANAGER_VERSION` instead of `:latest`, keeping all three oakestra-net components on the same release as the worker's NetManager binary. Running one of the three live against the other two still pinned reintroduces that same skew - keep an oakestra-net checkout at the pinned tag, or make all three live together.
- `override-live-*.yml`/`override-debug-*.yml` (`oak-dev up`/`reload`/`debug`): bind-mount host Python source (gunicorn `--reload`) or a cross-compiled host Go binary (directory mount only - a file mount keeps resolving the old inode) over the baked image. Paths must be absolute (`${OAKESTRA_REPO}`/`${OAKESTRA_NET_REPO}`/`${OAK_DEV_ROOT}`) - compose resolves relative paths against the *first* `-f` file's directory, not the invoking cwd.
- Unlike `system_manager`/`cluster_manager`, `root_service_manager`/`cluster_service_manager` don't run under gunicorn upstream at all - `service_manager.py` calls `eventlet.wsgi.server(...)` directly, and neither image has gunicorn installed. Their live overrides `pip install` it at container start and run `gunicorn --reload -k eventlet`. Every `override-debug-*.yml` for a Python component installs `debugpy` the same way, from `command:` - opt-in tooling, not a production dependency, and it *has* to happen at container start: `oak-dev debug` force-recreates the container, so an up-front `docker compose exec pip install` would land in a writable layer thrown away before the debugger ever runs. `root_service_manager` additionally needs `compose/shims/oak_root_sm.py`: its `mongo_init(app)` call lives inside `if __name__ == "__main__":`, so a bare `gunicorn service_manager:app` would never run it - the shim imports the app and calls `mongo_init` itself before gunicorn starts serving. `cluster_service_manager` doesn't need this; its `mongo_init`/`mqtt_init` already run at module import time.
- `netmanager`'s live/debug overrides target the `worker` service, same as `nodeengine` - they share one container. `oak-dev reload netmanager` restarts NetManager *and* nodeengined (it only registers with NetManager's unix socket once, at startup), which drops the overlay tunnel/proxy tables; already-running instances may need redeploying after.

### Worker container internals (`worker/docker-entrypoint.sh`)

Startup order is load-bearing:

1. Wait for cluster_manager TCP (no cross-project `depends_on` exists).
2. Remove stale pid/socket files - after a nodeengined crash the container restarts with the old writable layer, and a leftover containerd socket makes existence checks pass before containerd listens.
3. Start standalone containerd and wait until `ctr version` answers. NodeEngine dials `/run/containerd/containerd.sock` exactly once at startup; if that dial fails it keeps a nil client and later panics on the first deploy (upstream bug). A socket-file check is NOT sufficient.
4. `socat` forwards cluster port 10100 and MQTT 10003 to localhost - NodeEngine uses a single cluster address for both, but they are separate containers.
5. Start NetManager under its own supervised restart loop (`supervise_netmanager`, backgrounded), wait for its unix socket, then run `nodeengined` in a second supervised restart loop (not `exec`'d) - `oak-dev reload netmanager`/`reload nodeengine` send a `pkill -x NetManager`/`pkill nodeengined` and the relevant loop relaunches it without recreating the container. `OAK_DEV_DEBUG=1`/`OAK_DEV_DEBUG_NETMANAGER=1` swap in `dlv exec` in the matching loop (NetManager's Delve listens on :2346 inside the container, not :2345 - nodeengined's debug listener already owns that port, and both can be debugged at once).
   - Both loops use `if wait "$PID"; then code=0; else code=$?; fi` rather than a bare `wait "$PID"`: the script runs under `set -e`, and a bare `wait` returning the child's non-zero exit status (any crash, or even the SIGTERM from a normal `pkill` reload) would abort the *entire script* - not just report failure - killing the container instead of relaunching the process. `set -e` exempts commands used as an `if`/`while` condition, which is why this form survives it. The `TERM`/`INT` traps that propagate container shutdown down to whichever process is currently running need the same `|| true` treatment on each step for the same reason.

`/var/lib/containerd` is an anonymous volume (see `compose/worker.yml`): containerd overlayfs snapshots cannot be created on the container's own overlayfs (rootfs mounts fail with `invalid argument`). Workloads run in containerd namespace `oakestra`.

### E2E suite (`e2e/`)

The suite lives behind `//go:build e2e` (`e2e/doc.go` has the full package
doc); the HTTP client, SLA structs and polling it's built on live in
`internal/oakapi`, which carries no build tag and is unit-tested in CI on a
runner with no Docker.

- Files are numbered (`01_health` → `02_registration` → `03_deployment` →
  `04_network` → `05_failures`) and Go compiles/runs a package's top-level
  tests in sorted-filename order, so a plain run reproduces that order. But
  that's fail-fast ergonomics only - the ordering that actually matters
  (deploy before scale before undeploy; web before client) is structural,
  encoded as ordered subtests within one top-level test via
  `if !t.Run(name, fn) { return }` (see `TestDeploymentLifecycle`,
  `TestOverlayNetwork`). A bare `t.Run` without the `if` doesn't stop the
  sequence on failure - don't drop the guard when adding a step.
- `TestOverlayNetwork` proves the overlay data plane: it requests a fixed RR
  service IP in the SLA (`10.30.30.30`) and uses a `one_shot` busybox client
  whose exit code becomes the verdict (exit 0 → NodeEngine reports
  `COMPLETED`) - a client container that silently never ran would otherwise
  look identical to a pass. `TestFailureReporting` expects failure statuses,
  so it deliberately never calls `(*Job).NotFailed()` - that helper exists to
  abort a poll early, and here failure is the thing under test.
- `oakapi.Poll` retries every error except one built with `oakapi.Terminal`,
  which aborts immediately - the Go equivalent of the pytest suite's
  `AssertionError`-propagates-but-everything-else-retries contract.
  `(*Job).NotFailed()` returns a `Terminal` error when a job hits a status in
  `oakapi.FailureStatuses`, so a poll checking it stops within one interval
  of a terminal state instead of running out the full deploy timeout. Keep
  that distinction when adding a new poll: a "not ready yet" check must
  return a real error, never a zero value with a nil error, or a check that's
  always false looks exactly like a pass.
- `oakapi.Decode` unwraps double-encoded responses: several system_manager
  endpoints return `json_util.dumps(...)` through flask-smorest, producing a
  JSON string containing JSON rather than the object itself. Use it for any
  new API call.
- `oakapi.Client` re-logs-in on 401 (JWT expires after ~15 min) - and does it
  the Go-specific way that matters: a `*bytes.Reader` request body is
  consumed by its first send, so retrying by resending the same
  `*http.Request` silently replays an empty body and turns an expired token
  into a confusing 400 rather than a clean retry. `attempt` in
  `internal/oakapi/client.go` rebuilds a fresh request from the held
  `[]byte` on every attempt for exactly this reason - don't "simplify" a
  retry back to reusing one `*http.Request`.
- Nothing in this package calls `t.Parallel`, anywhere, ever - every test
  contends for the same one worker node and cluster, and concurrent subtests
  would race deploys/scales/capacity against each other.
- `-count=1` and `-timeout` on the `go test` invocation are not cosmetic: `go
  test` caches a successful result keyed on package files and env vars read,
  not network I/O, so a second run against a stack that's since broken can
  replay a stale PASS in milliseconds without `-count=1`; the derived
  `-timeout` (see `internal/testsuite.derivedTimeout`) exists because the
  default 10 minutes is shorter than this suite's own worst-case polling
  budget, and blowing it prints a goroutine dump instead of a readable test
  failure.
- `TestSmokeHealth` and `TestSmokeRegistration` are the only two tests
  matching `-run '^TestSmoke'`. The `TestSmoke` prefix on a function name IS
  the entire smoke-mode contract - there is no separate list to keep in
  sync, so a new smoke-safe test opts in purely by being named that way.
- SLA/microservice structs (`internal/oakapi/sla.go`) must not gain a blanket
  `,omitempty` and must keep `Cmd`/`AddedFiles`/`Constraints` initialized to
  non-nil empty slices, never `nil` - a nil Go slice marshals to `null`, not
  `[]`, which the pytest suite's SLA payloads never sent. Both are enforced
  by golden-JSON tests in `internal/oakapi/sla_test.go`; if you touch that
  file's marshalling, check the goldens still assert `"cmd":[]` and every
  zero-valued field present rather than dropped.
- Config via env (see `.env.example`): `OAK_ROOT_API`/`OAK_CLUSTER_API`/
  `OAK_ROOT_RA`/`OAK_USERNAME`/`OAK_PASSWORD`/`OAK_READY_TIMEOUT`/
  `OAK_DEPLOY_TIMEOUT`, resolved with the usual process env > `.env` >
  `oak-dev.yaml` > default precedence by `config.LoadE2E` (see
  `internal/config/config.go`'s `resolveE2E`) - and unlike the pytest suite,
  `.env` now genuinely reaches the tests.

## Gotchas

- This is a multi-repo workspace (this repo, `../oakestra`, `../oakestra-net`, often alongside `oakestra-deploy`) - the shell's cwd persists across Bash calls independent of whichever repo the session was launched in. Verify with `pwd`/`git status` before any mutating command (`go get`, `go mod tidy`, git ops).
- **Never restart/recreate the worker while deployment tests run.** Each new worker container registers a new node ID; instances scheduled to the old ID stay `NODE_SCHEDULED` forever and the tests time out. Stale node candidates from previous worker containers linger in the cluster DB until they age out.
- `NETMANAGER_VERSION` defaults to `alpha-` + `$OAKESTRA_REPO/version.txt` - oakestra-net publishes develop builds only under `alpha-` tags; plain version tags may not exist.
- Local changes to `oakestra` Python services, scheduler, and NodeEngine are tested directly, and (via `OAKESTRA_NET_REPO`) so can the three oakestra-net components (`root_service_manager`/`cluster_service_manager`/`netmanager`) - they just default to GHCR images/release binaries since most changes here don't touch that layer. The shared Python libraries are installed from the `LIB_BRANCH` GitHub branch at image build time regardless - local library edits are invisible until pushed.
- `oak-dev reload rsm`/`reload csm --image` errors on purpose (`Component.PrebuiltImage`): those two run pinned GHCR images with no local `build:` section, so there's nothing for `docker compose build` to do - a plain `oak-dev reload rsm` already picks up source edits via `gunicorn --reload`.
- The E2E suite needs all three stacks, so `oak-dev test` refuses to run under a narrowed scope (including a sticky one left by an earlier `up --stack worker`) instead of failing later as an opaque connection timeout. `--smoke` is no different: it still checks the root API and worker registration. `oak-dev test <component>` (`go test ./...`) is unaffected - it touches no containers.
- A debug overlay is not part of the rendered topology, so `oak-dev debug` records what's attached in `.generated/debug` (`internal/debugstate`) and reapplies *every* active `override-debug-*.yml` for a container each time it recreates one. Without that, `debug nodeengine` followed by `debug netmanager` detached the first: both are the `worker` service. `reload`/`up`/`down` recreate from the plain topology and clear the corresponding entries - only for what they actually recreated (`up`/`down` per stack, `reload` per container, via `forgetDebug`), since `up --stack root` leaves a debugged worker running and recreating one container detaches every debugger in it. Entries are `<component> <stack>` pairs for that reason.
- `oak-dev reload <go-component> --image` also cross-compiles the host binary when that component is live: `/oak-bin` shadows whatever the rebuild bakes into the image, so rebuilding alone would recreate the container onto the *old* host binary and report success.
- `oak-dev down --stack worker --volumes` is worth preferring over a plain `down` when recreating the worker: its anonymous containerd volumes otherwise accumulate one per recreation.
- `resource_abstractor.py` hardcodes `app.run(..., debug=False)` - `FLASK_DEBUG=TRUE` does nothing for it (unlike system_manager/cluster_manager, already on gunicorn). Live-reload needs wrapping in `gunicorn --reload` too.
- Worker node ID stability is decided by `cluster_manager`, not NodeEngine: `PUT /api/v1/resources` dedups registration by `candidate_name` (the reported hostname). ID reuse across a restart only holds as long as the *container* (hence hostname) isn't recreated.
- Compose `command:`/`environment:` overrides can't reference a sibling field like `${RESOURCE_ABSTRACTOR_PORT}` - `${VAR}` interpolates against the host shell env at `docker compose` invocation time, not the container's own `environment:` list. Hardcode the value instead.
- `docker info --format '{{.Architecture}}'` reports GNU arch names (`aarch64`/`x86_64`); Go/`uname -m` on Apple Silicon reports `arm64`. Normalize both before comparing.
- A stale exited container from an earlier session can reference a since-deleted Docker network ID, making `restart`/`up` fail with `network ... not found`. Not a code bug - `docker rm -f` the stale container(s) and retry.
- `oak` (oakestra-cli) SLA files are JSON, not YAML (`oak application create -f fixtures/nginx.json`). Config keys: `oak config set system_manager_ip/cluster_manager_ip/cluster_name/cluster_location`, `oak config credentials <user> <pass>`.
- `oak-dev dev`'s file watcher is in-process (`internal/watch`, fsnotify-based), replacing a former external `watchexec` dependency - don't reintroduce a shelled-out watcher; extend `internal/watch` instead.
