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

- `oak-dev up [--workers N]` - build and start the stacks in scope.
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
- `oak-dev debug <component> [--no-live] [--stack ...]` - recreate the
  component with a debugger attached: Delve for Go, debugpy for Python. Prints
  the localhost port, which matches `.vscode/launch.json`'s "Attach: <component>"
  configs. A component running in more than one stack (the scheduler, in both
  root and cluster) needs `--stack` to disambiguate.
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
  resolved setting; `get`/`set` target one. `.env`/the process environment
  outrank `oak-dev.yaml` for `oakestra_repo`, `cluster.name`,
  `cluster.location`, `versions.netmanager`, `versions.lib_branch`.
- `oak-dev completion [bash|zsh|fish|powershell]` / `completion install
  [shell]` - shell completion, plus writing the script straight to where the
  shell loads it from.
- `oak-dev skill install|status|uninstall [--global] [--target ...]` - installs
  this skill into `.claude/skills` / `.agents/skills` (project by default,
  `--global` for `~`). Report-only in `oak-dev doctor` - never installed as a
  side effect of `--fix`.

## Debugger ports (`.vscode/launch.json`)

Delve (Go): scheduler on the root stack listens on `:2345`, the cluster
scheduler on `:2346`, NodeEngine on `:2347`, NetManager on `:2348`. debugpy
(Python): `cluster_manager` on `:5681`; other Python components follow the same
pattern - check `.vscode/launch.json` for the exact port before attaching.

Any number of components can be debugged at once, including NodeEngine and
NetManager together (separate processes in the shared worker container -
oak-dev reapplies every attached overlay when it recreates a container, so
attaching the second no longer detaches the first).
