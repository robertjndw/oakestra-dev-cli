# Troubleshooting

Runtime failure modes you can hit while operating the stack - not
architecture (that's `AGENTS.md` in the `oakestra-macos-testing` checkout,
for anyone changing oak-dev itself).

## Never restart/recreate the worker while deployment tests run

Each new worker container registers a new node ID. Instances already
scheduled to the old ID stay `NODE_SCHEDULED` forever and the test suite
times out. `oak-dev dev`'s watchers already guard against this via a lock
file (`internal/testsuite.IsLocked`); `oak-dev debug` on a worker-resident
component checks the same lock. If you see instances stuck in
`NODE_SCHEDULED`, check whether the worker was recreated mid-test - stale
node candidates from previous worker containers linger in the cluster DB
until they age out.

## A worker won't register / overlay traffic silently times out

- Check `oak-dev status` first - it shows clusters and nodes via the `oak`
  CLI if it's on PATH and installed (`oak-dev doctor --fix` configures it).
- If registration succeeds but deployed instances look `RUNNING` yet traffic
  never arrives, this is very likely the cluster_service_manager shared-netns
  IP-authorization issue baked into `compose/override-cluster-servicemanager.yml`
  - not a new regression. Confirm the override files still exist rather than
    debugging the symptom from scratch.

## `reload rsm` / `reload csm --image` errors on purpose

Those two run pinned upstream GHCR images with no local `build:` section, so
there's nothing for `docker compose build` to do. A plain `oak-dev reload
rsm` (no `--image`) already picks up source edits via `gunicorn --reload`.

## `reload netmanager` / `reload nodeengine` restart both

NetManager and NodeEngine share one worker container. `oak-dev reload
netmanager` restarts NetManager *and* nodeengined (NodeEngine only registers
with NetManager's unix socket once, at startup), which drops the overlay
tunnel/proxy tables. Already-running deployments may need redeploying
afterward.

## Coming back from `oak-dev debug` on a worker-resident component

`oak-dev reload` deliberately never recreates the worker container (it would
mint a new node ID). If `debug nodeengine`/`debug netmanager` was used, undo
it with `oak-dev up --stack worker`, not `reload` - `debug` itself warns
about this when it runs.

## Stale container referencing a deleted Docker network

An exited container from an earlier session can reference a since-deleted
Docker network ID, making `restart`/`up` fail with `network ... not found`.
Not a code bug - find and `docker rm -f` the stale container(s), then retry.

## containerd volumes accumulating

`/var/lib/containerd` is an anonymous volume per worker container recreation.
Prefer `oak-dev down --stack worker --volumes` over a plain `down` when
recreating the worker, to avoid accumulating one per recreation.
`oak-dev doctor` also flags this (a non-empty `oakestra` network with leftover
worker volumes).

## `NETMANAGER_VERSION` / oakestra-net version skew

`NETMANAGER_VERSION` defaults to `alpha-<version.txt in $OAKESTRA_REPO>` -
oakestra-net publishes develop builds only under `alpha-` tags, so a plain
version tag may not exist. Running one of `root_service_manager` /
`cluster_service_manager` / `netmanager` live against the other two still
pinned to GHCR images reintroduces version skew between them - either keep an
`oakestra-net` checkout at the pinned tag, or make all three live together.

## Host/Docker architecture mismatch

`docker info --format '{{.Architecture}}'` reports GNU arch names
(`aarch64`/`x86_64`); Go/`uname -m` on Apple Silicon reports `arm64`.
`oak-dev doctor` already normalizes both before comparing - if it reports a
mismatch, check the Docker engine's platform settings (OrbStack/Docker
Desktop sometimes omit `TARGETARCH`).

## SLA / `oak` CLI format

`oak` (oakestra-cli) SLA files are JSON, not YAML:
`oak application create -f fixtures/nginx.json`. Config keys: `oak config set
system_manager_ip/cluster_manager_ip/cluster_name/cluster_location`, `oak
config credentials <user> <pass>` - `oak-dev doctor --fix` sets these to
match the local stack automatically.
