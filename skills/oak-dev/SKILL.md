---
name: oak-dev
description: >-
  Run, debug and test a local Oakestra deployment with the `oak-dev` CLI: start
  and stop the root orchestrator, cluster orchestrator and dockerized worker,
  hot-reload a component from a local oakestra/oakestra-net checkout, attach a
  debugger, read container or MQTT logs, deploy a test app, and run the Go
  E2E suite. Use this whenever the user wants to run Oakestra locally, bring
  the stack up or down, reload or debug system_manager / cluster_manager /
  scheduler / resource abstractors / NodeEngine / NetManager / service
  managers, run or narrow the E2E tests, or diagnose a stuck deployment, a
  worker that will not register, an instance stuck in NODE_SCHEDULED, or an
  edit that reloads but changes nothing - even if oak-dev is never mentioned
  by name. Use it too for VS Code launch configs, extra workers, a local
  oakestra_utils_library mount, and the dashboard/observability profiles, and
  when a change in the oakestra, oakestra-net or oakestra-deploy repos needs
  verifying against a running stack.
compatibility: Requires macOS with Docker (OrbStack), the oak-dev CLI on PATH, and an oakestra-dev-cli checkout
license: Apache-2.0
---

# oak-dev

`oak-dev` runs the Oakestra root orchestrator, cluster orchestrator and a
dockerized worker as three Docker Compose projects joined on one shared
`oakestra` network, built from a local `oakestra` checkout (and optionally
`oakestra-net`, for the overlay-networking components). It lets you edit that
source and see the change live without rebuilding images.

## Locating the CLI

`oak-dev` refuses to run outside an `oakestra-dev-cli` checkout (it
looks for `compose/worker.yml`), which matters because most sessions happen
while editing a **different** repo (`oakestra`, `oakestra-net`,
`oakestra-deploy`).

1. Check the binary exists: `command -v oak-dev`. If missing, find the
   `oakestra-dev-cli` checkout and run `make install` in it.
2. If the current directory is not that checkout, find it - conventionally a
   sibling directory of the repo you're in - and pass it with the persistent
   `-C` flag on every command: `oak-dev -C ../oakestra-dev-cli status`.
3. If several candidates exist or none can be found, ask rather than guess.

`oak-dev skill` is the one exception - it resolves its own paths and works
from anywhere without `-C`.

## Running oak-dev from an agent session

The CLI is written for a human at a terminal, so a few commands behave badly
when something non-interactive drives them. Each of these fails by hanging or
by quietly doing nothing rather than by erroring, which is what makes them
worth knowing before you hit them:

- **`dev` and `logs` stream until Ctrl-C.** Run in the foreground they never
  return. Use `oak-dev logs <target> --no-follow --tail 200` to read logs and
  exit; use `oak-dev up` plus explicit `oak-dev reload <component>` instead of
  `oak-dev dev`. Reach for `dev` only when the user is going to watch it, and
  run it in the background.
- **`up`, `test`, and `reload --image` take minutes**, not seconds - a cold
  `up` builds images and pulls upstream ones, and the E2E suite waits on real
  container startup and image pulls. Give them a long timeout (10+ minutes) or
  run them in the background; a tool timeout mid-build leaves a half-built
  stack that is slower to diagnose than the wait would have been.
- **`shell <target>` without `--` opens an interactive prompt.** Always pass
  the command: `oak-dev shell worker -- ctr -n oakestra containers ls`.
- **`down --volumes` and `reset` prompt for confirmation**, and a prompt read
  from a closed stdin counts as "no" - so they quietly do nothing rather than
  failing loudly. Pass `--yes` when the destruction is genuinely intended, and
  confirm with the user first: both throw away state (`--volumes` wipes the
  databases and the worker's containerd images) that a rebuild cannot restore.

Prefer `oak-dev status` over `docker ps` and `oak-dev logs` over
`docker compose logs`: the three compose projects each need their own `-f`
chain, and the raw commands miss the other two.

## Task router

| User wants to... | Run |
|---|---|
| Start everything | `oak-dev up` (add `--workers N` for N dockerized workers) |
| Start only part of the stack | `oak-dev up --stack root\|cluster\|worker` (sticky until the next plain `down`) |
| Stop everything | `oak-dev down` (`--volumes` for a genuinely fresh start, prompts first) |
| Fix "stuck"/weird state without a full restart | `oak-dev reset` (drops DBs, restarts services, ~15s, keeps images and the worker's node ID) |
| See if Oakestra is healthy | `oak-dev status` |
| Start working: watch, rebuild, stream logs | `oak-dev dev [component...]` |
| Apply an edit to a running component | `oak-dev reload <component>` |
| Attach a debugger | `oak-dev debug <component>` |
| Run the E2E suite | `oak-dev test` (`--smoke` for health+registration only) |
| Run one Go component's unit tests | `oak-dev test <component> [-- go test args]` |
| Read logs | `oak-dev logs [target...]` (`mqtt` for the control-plane broker) |
| Get a shell / mongosh | `oak-dev shell <target>` (`mongo-root`, `mongo-cluster`) |
| Check prerequisites / bootstrap | `oak-dev doctor` (`--fix` to repair what it can) |
| Read or change oak-dev.yaml | `oak-dev config [get\|set <key> [value]]` |
| Set up VS Code F5 debugging | `oak-dev vscode install [dir...]` (`--all` for all three repos) |
| Install this skill somewhere else | `oak-dev skill install [--global]` |

Every command accepts `-C <path>` and `--stack full|root|cluster|worker`
(persistent flags on the root command).

## Settings worth knowing about

`oak-dev config set <key> <value>` writes into `oak-dev.yaml`; `.env` and the
process environment outrank it for the keys that have an env var. The full
list is `oak-dev config` (no arguments), but three are easy to miss because
nothing prompts you towards them:

| Key | Why you'd reach for it |
|---|---|
| `libs_repo` | Path to a local `oakestra_utils_library` checkout. Without it the shared Python libraries come from the `versions.lib_branch` GitHub branch at **image build time**, so local library edits are invisible no matter how many times you reload. |
| `profiles.dashboard` / `profiles.observability` / `profiles.addons` | This repo ships lean compose overrides with all three off. Flip one on and `oak-dev up` adds the upstream stack back. |
| `workers` | Number of dockerized workers (also `oak-dev up --workers N`, for one run). More than one is what you want for scheduler/placement work. |

## Component naming

Components take a full name, an alias, or any unambiguous prefix:

| Full name | Alias | Runtime |
|---|---|---|
| `system_manager` | `sm` | Python |
| `jwt_generator` | `jwt` | Python |
| `root_resource_abstractor` | `rra` | Python |
| `cluster_manager` | `cm` | Python |
| `cluster_resource_abstractor` | `cra` | Python |
| `scheduler` | `sched` | Go |
| `nodeengine` | `ne` | Go |
| `root_service_manager` | `rsm` | Python (oakestra-net; pinned image, no local build) |
| `cluster_service_manager` | `csm` | Python (oakestra-net; pinned image, no local build) |
| `netmanager` | `nm` | Go (oakestra-net; runs inside the worker container) |

## Core iteration loop

Edit source in the `oakestra` (or `oakestra-net`) checkout, then:

```
oak-dev reload <component>     # cross-compile + in-place restart (Go, ~5s)
                                # or a no-op notice (Python already live under gunicorn --reload)
oak-dev test --smoke           # or: oak-dev logs <component>
```

`reload` cross-compiles Go components and swaps the binary without recreating
the container (preserves the worker's node ID); Python components are already
bind-mounted under `gunicorn --reload`, so a plain `reload` just confirms that.
Use `reload <component> --image` only for changes no mount can pick up -
`requirements.txt`, a `Dockerfile`, `go.mod`. `reload rsm`/`reload csm --image`
error on purpose: those two run pinned upstream images with no local `build:`
section.

`oak-dev dev [component...]` does this automatically: starts the stack if
needed, watches every live Go component's source, and streams every
container's logs in one aggregated, prefixed view. Narrowing it to specific
components only limits the watchers, not the logs. It runs until Ctrl-C, so
it suits a human at a terminal rather than a scripted sequence.

One case defeats `reload` entirely: edits to the shared
`oakestra_utils_library`. Those libraries are pip-installed from a GitHub
branch when the image is built, so nothing in the running container points at
your checkout. Set `libs_repo` (see above) and `oak-dev up` to bind-mount it
over site-packages - the mount is generated for the Python components already
in `live:`, so add the ones you care about first. Otherwise the only way to
see the change is to push the branch and rebuild the image.

## When things go wrong

See [references/troubleshooting.md](references/troubleshooting.md) for
diagnosing a stuck deployment, a worker that will not register, or an
instance stuck in `NODE_SCHEDULED` - and for the runtime gotchas (stale
Docker networks, containerd volume accumulation, version skew) that are easy
to mistake for a real bug.

For the full flag reference, single-test invocations, and the debugger/port
mapping, see [references/commands.md](references/commands.md).

For narrower workflows - proving a change reached the running system, partial
stacks, single-test runs, working on the shared Python libraries, turning
the dashboard/observability profiles back on - see
[references/workflows.md](references/workflows.md).
