---
name: oak-dev
description: >-
  Run, debug and test a local Oakestra deployment with the `oak-dev` CLI: start
  and stop the root orchestrator, cluster orchestrator and dockerized worker,
  hot-reload a component from a local oakestra/oakestra-net checkout, attach a
  debugger, stream logs, and run the pytest E2E suite. Use this whenever the
  user wants to run Oakestra locally, bring the stack up or down, reload or
  debug system_manager / cluster_manager / scheduler / resource abstractors /
  NodeEngine / NetManager / service managers, run or narrow the E2E tests, read
  container or MQTT logs, or diagnose a stuck deployment, a worker that will
  not register, or an instance stuck in NODE_SCHEDULED - even if oak-dev is
  never mentioned by name. Also use it when working in the oakestra,
  oakestra-net or oakestra-deploy repos and a change needs verifying against a
  running stack.
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

Every command accepts `-C <path>` and `--stack full|root|cluster|worker`
(persistent flags on the root command).

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
components only limits the watchers, not the logs.

## When things go wrong

See [references/troubleshooting.md](references/troubleshooting.md) for
diagnosing a stuck deployment, a worker that will not register, or an
instance stuck in `NODE_SCHEDULED` - and for the runtime gotchas (stale
Docker networks, containerd volume accumulation, version skew) that are easy
to mistake for a real bug.

For the full flag reference, single-test invocations, and the debugger/port
mapping, see [references/commands.md](references/commands.md).

For narrower workflows - partial stacks, single-file test runs, debugging a
worker-resident component - see [references/workflows.md](references/workflows.md).
