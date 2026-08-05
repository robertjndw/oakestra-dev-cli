# Command reference

Full flag reference for every `oak-dev` subcommand. `oak-dev <command> --help`
is always authoritative if this drifts.

## Persistent flags (every command)

- `--stack full|root|cluster|worker` - limit the command to a scope. Default
  comes from `oak-dev.yaml`'s `stack:`. The scope passed to `up` is sticky
  (written to `.generated/stack`) and reused by later commands until the next
  plain `down` clears it.
- `-C, --repo-root <path>` - run as if invoked from this
  `oakestra-dev-cli` checkout. Required whenever the current directory
  is a different repo.

## Start and stop

- `oak-dev up [--workers N]` - build and start the stacks in scope. `--workers`
  overrides the `workers` setting for this run. Above 1 it scales the single
  `worker` compose service rather than declaring new ones, which is worth
  knowing because `logs`/`shell` targets are compose *service* names: `oak-dev
  logs worker` follows every replica, but `oak-dev shell worker` only ever
  lands in the first. To reach a specific replica, find its container with
  `docker ps` and `docker exec` into it directly.
- `oak-dev down [--volumes] [--yes]` - stop, in reverse start order.
  `--volumes` also deletes MongoDB/Redis/containerd volumes (prompts unless
  `--yes`). A plain `down` (no `--stack`) also clears the sticky scope.
- `oak-dev reset [--yes]` - drops every non-system database in root+cluster
  mongo, flushes both redis instances, restarts app-level services. Does not
  touch images, NetManager databases, or the worker's node ID/instances.

## Work on the code

- `oak-dev dev [component...] [--test smoke] [--no-up]` - start (unless
  `--no-up`), watch every live Go component's source, aggregate all logs.
  `--test smoke` re-runs the smoke suite after each successful rebuild (never
  the full suite - deployment tests are order-dependent). The worker is
  skipped automatically while a test run holds the lock.
- `oak-dev reload [component...] [--image] [--no-live]` - apply source edits
  to the running stack. No arguments reloads everything in `live:`. Naming a
  component not yet in `live:` adds it automatically (recreating that
  container once) unless `--no-live`. `--image` rebuilds the image instead of
  swapping the binary/relying on the bind mount - use it for
  `requirements.txt`/`Dockerfile`/`go.mod` changes.
- `oak-dev debug <component> [--no-live] [--stack ...] [--wait]` - recreate
  the component with a debugger attached: Delve for Go, debugpy for Python.
  Prints the localhost port, which matches `oak-dev vscode install`'s
  generated launch.json ("oak-dev: attach <component>"). A component running
  in more than one stack (the scheduler, in both root and cluster) needs
  `--stack` to disambiguate. `--wait` blocks until the port actually accepts
  connections instead of returning as soon as the container is recreated -
  what a generated launch config's `preLaunchTask` uses so VS Code's F5
  doesn't race the debugger starting up.
- `oak-dev test [component] [-- args...] [--smoke] [--no-up]` - with no
  component, runs the pytest E2E suite (starting the stack first unless
  `--no-up`). With a Go component, runs `go test ./...` on the host, no
  Docker, ~2s. `--smoke` limits the E2E suite to health + registration.
  Anything after `--` passes straight through to pytest/`go test`.
  The E2E suite needs all three stacks and refuses to start under a narrowed
  scope - if it reports one, re-run as `oak-dev test --stack full`. That
  applies to `--smoke` too; only the per-component `go test` path is exempt.

## Look inside

- `oak-dev logs [target...] [--tail N] [--no-follow]` - no argument merges
  every stack in scope. A target can be a stack name, a component (name,
  alias, or prefix), a raw container name, or `mqtt` (subscribes to the
  broker instead of reading stdout - the only way to watch control-plane
  messages like `nodes/<id>/control/deploy`).
- `oak-dev shell <target> [-- cmd...]` (alias `sh`) - opens bash, falling back
  to sh. Target can be a component, a raw container name, or the named
  endpoints `mongo-root` / `mongo-cluster` (open `mongosh` instead of a
  shell). Anything after `--` runs instead of a shell.

## Set up

- `oak-dev doctor [--fix]` - preflight checks (OAKESTRA_REPO checkout, docker
  compose version, kernel/mongo pin, host/docker arch match, proto stubs, Go
  toolchain, `oak` CLI, stale networks/volumes). `--fix` generates missing
  proto stubs and points the `oak` CLI at this stack; both write outside this
  repo and announce every file/setting touched.
- `oak-dev config [get <key> | set <key> <value>]` - no arguments prints every
  resolved setting; `get`/`set` target one. `set` preserves the comments and
  formatting in `oak-dev.yaml`, and warns when an env override means the write
  won't take effect. Every accepted key:

  | Key | Env override | Meaning |
  |---|---|---|
  | `oakestra_repo` | `OAKESTRA_REPO` | the `oakestra` checkout to build from |
  | `libs_repo` | `OAKESTRA_LIBS_REPO` | optional `oakestra_utils_library` checkout to mount over site-packages in live Python containers |
  | `cluster.name` | `CLUSTER_NAME` | cluster registered with the root orchestrator |
  | `cluster.location` | `CLUSTER_LOCATION` | `"lat,lon,radius"` |
  | `workers` | - | number of dockerized workers |
  | `stack` | `OAK_DEV_STACK` | default scope; a sticky scope from `up --stack` still outranks it |
  | `profiles.dashboard` | - | add the upstream dashboard back |
  | `profiles.observability` | - | add the upstream observability stack back |
  | `profiles.addons` | - | add the upstream addons back |
  | `versions.netmanager` | `NETMANAGER_VERSION` | `auto` = `alpha-<version.txt>`, or an explicit tag |
  | `versions.lib_branch` | `LIB_BRANCH` | branch the shared Python libraries are installed from at image build time |

  `oakestra_net_repo` and `live:` are not settable here: `live:` is managed by
  `reload`/`debug`, and both are still editable by hand in `oak-dev.yaml`.
- `oak-dev completion [bash|zsh|fish|powershell]` / `completion install
  [shell]` - shell completion, plus writing the script straight to where the
  shell loads it from.
- `oak-dev skill install|status|uninstall [--global] [--target ...]` - installs
  this skill into `.claude/skills` / `.agents/skills` (project by default,
  `--global` for `~`). Report-only in `oak-dev doctor` - never installed as a
  side effect of `--fix`.
- `oak-dev vscode install|status|uninstall [dir...] [--all] [--dry-run]
  [--yes]` - generates `.vscode/launch.json` and `.vscode/tasks.json` from the
  component registry into `dir` (current directory by default), merging with
  anything already there - only entries whose name/label starts with
  `oak-dev: ` are regenerated. `--all` targets this checkout plus
  `$OAKESTRA_REPO` and `$OAKESTRA_NET_REPO` at once. Each generated launch
  config's `preLaunchTask` runs `oak-dev debug <component> --wait`, so F5
  both attaches and starts the debugger with one keystroke. `uninstall`
  prompts before rewriting/deleting a file with a hand-written comment it
  can't preserve, unless `--yes`.

## Debugger ports (`oak-dev vscode install`'s launch.json)

| Component | Port | Debugger |
|---|---|---|
| `scheduler` (root stack) | 2345 | Delve |
| `scheduler` (cluster stack) | 2346 | Delve |
| `nodeengine` | 2347 | Delve |
| `netmanager` | 2348 | Delve |
| `system_manager` | 5678 | debugpy |
| `jwt_generator` | 5679 | debugpy |
| `root_resource_abstractor` | 5680 | debugpy |
| `cluster_manager` | 5681 | debugpy |
| `cluster_resource_abstractor` | 5682 | debugpy |
| `root_service_manager` | 5683 | debugpy |
| `cluster_service_manager` | 5684 | debugpy |

`oak-dev debug <component>` prints the port it used, and `DebugPort` in
`internal/components/components.go` is the registry both this table and the
generated launch.json come from - check there if they ever disagree.

Any number of components can be debugged at once, including NodeEngine and
NetManager together (separate processes in the shared worker container -
oak-dev reapplies every attached overlay when it recreates a container, so
attaching the second no longer detaches the first).
