# Contributing

This is background knowledge for working on `oakestra-dev-cli` itself -
the `oak-dev` CLI, the compose overrides, and the worker image. If you're
just using the stack to test changes to `oakestra`, you want
[README.md](README.md) instead. `CLAUDE.md` goes deeper still (exact file
paths, line-level rationale) for AI-assisted work in this repo.

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

See `CLAUDE.md`'s "Architecture" and "Why the local overrides exist" sections
for the full detail behind each of these - exact env vars, port numbers, and
the failure modes each override fixes.
