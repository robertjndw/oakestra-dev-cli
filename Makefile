# ─── Oakestra local testing on macOS ─────────────────────────────────────────
# Runs the root + cluster orchestrators from a local oakestra checkout and a
# dockerized worker node, then exercises the whole stack with an E2E test
# suite. All stacks share the 'oakestra' Docker network, so service hostnames
# resolve across compose projects without any IP configuration.
#
# Quickstart:
#   cp .env.example .env   # once, edit as needed
#   make e2e               # build + start everything + run the test suite
#
# Day-to-day:
#   make up                # start root + cluster + dockerized worker
#   make test              # run the E2E suite against the running stack
#   make rebuild s=system_manager
#   make down
# ─────────────────────────────────────────────────────────────────────────────

# Load local overrides (.env is gitignored)
-include .env

# Path to the local oakestra checkout (sibling directory by default)
OAKESTRA_REPO ?= ../oakestra
override OAKESTRA_REPO := $(abspath $(OAKESTRA_REPO))
export OAKESTRA_REPO

# Defaults - work as-is for a single-machine setup
export SYSTEM_MANAGER_URL    ?= system_manager
export CLUSTER_NAME          ?= test-cluster
export CLUSTER_LOCATION      ?= 52.5200,13.4050,100
export LIB_BRANCH            ?= develop
# NetManager release baked into the dockerized worker. version.txt on develop
# tracks the next release, which only exists as an 'alpha-' tag in oakestra-net.
export NETMANAGER_VERSION    ?= alpha-$(shell cat $(OAKESTRA_REPO)/version.txt 2>/dev/null || echo v0.4.411)

# ── Compose command definitions ───────────────────────────────────────────────
# Lean config: no addons, no observability stack, no dashboard
ROOT_COMPOSE := docker compose \
    -f $(OAKESTRA_REPO)/root_orchestrator/docker-compose.yml \
    -f $(OAKESTRA_REPO)/root_orchestrator/override-no-addons.yml \
    -f $(OAKESTRA_REPO)/root_orchestrator/override-no-observe.yml \
    -f $(OAKESTRA_REPO)/root_orchestrator/override-no-dashboard.yml \
    -f compose/override-root-mongo.yml \
    -f compose/override-root-servicemanager.yml

CLUSTER_COMPOSE := docker compose \
    -f $(OAKESTRA_REPO)/cluster_orchestrator/docker-compose.yml \
    -f $(OAKESTRA_REPO)/cluster_orchestrator/override-no-addons.yml \
    -f $(OAKESTRA_REPO)/cluster_orchestrator/override-no-observe.yml \
    -f compose/override-cluster-mongo.yml \
    -f compose/override-cluster-servicemanager.yml

# Optional: uncomment to build oakestra-net from local source instead of GHCR images
# ROOT_COMPOSE    += -f $(OAKESTRA_REPO)/root_orchestrator/override-local-service-manager.yml
# CLUSTER_COMPOSE += -f $(OAKESTRA_REPO)/cluster_orchestrator/override-local-service-manager.yml

WORKER_COMPOSE := docker compose -f compose/worker.yml

# Test suite
VENV   := .venv
PYTEST := $(VENV)/bin/pytest

# Name of the OrbStack Linux VM used as a real worker node (override via env or .env)
WORKER_VM ?= oak-worker

# Translate Mac arch to Go/Linux arch (arm64 stays arm64; x86_64 → amd64)
GOARCH := $(shell uname -m | sed 's/x86_64/amd64/')

.PHONY: help check-repo up down up-root up-cluster down-root down-cluster \
        logs-root logs-cluster log status open clean \
        rebuild rebuild-cluster rebuild-scheduler restart \
        worker-up worker-down worker-logs worker-shell worker-scale \
        venv test test-smoke e2e \
        vm-build vm-create vm-create-local vm-rebuild vm-up vm-down \
        vm-logs vm-shell vm-delete

help: ## Show available targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	    | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

check-repo:
	@[ -f "$(OAKESTRA_REPO)/version.txt" ] \
	    || (echo "ERROR: no oakestra checkout at $(OAKESTRA_REPO) - set OAKESTRA_REPO in .env"; exit 1)

# ── Orchestrator stacks ───────────────────────────────────────────────────────

up: check-repo up-root up-cluster worker-up ## Build + start root, cluster, and dockerized worker

down: worker-down down-cluster down-root ## Stop everything

up-root: check-repo ## Start root orchestrator
	$(ROOT_COMPOSE) up -d --build

up-cluster: check-repo ## Start cluster orchestrator
	$(CLUSTER_COMPOSE) up -d --build

down-root:
	$(ROOT_COMPOSE) down

down-cluster:
	$(CLUSTER_COMPOSE) down

# ── Dockerized worker ─────────────────────────────────────────────────────────

worker-up: check-repo ## Build + start the dockerized worker (DinD)
	$(WORKER_COMPOSE) up -d --build

worker-down: ## Stop the dockerized worker
	$(WORKER_COMPOSE) down -v

worker-scale: ## Run multiple workers: make worker-scale n=3
	@[ -n "$(n)" ] || (echo "Usage: make worker-scale n=<count>"; exit 1)
	$(WORKER_COMPOSE) up -d --build --scale worker=$(n)

worker-logs: ## Tail dockerized worker logs
	$(WORKER_COMPOSE) logs -f --tail=100

worker-shell: ## Shell into the dockerized worker
	$(WORKER_COMPOSE) exec worker bash

# ── Test suite ────────────────────────────────────────────────────────────────

venv: $(VENV)/bin/activate ## Create the test virtualenv

$(VENV)/bin/activate: tests/requirements.txt
	python3 -m venv $(VENV)
	$(VENV)/bin/pip install --quiet -r tests/requirements.txt
	@touch $(VENV)/bin/activate

test: venv ## Run the full E2E suite against the running stack
	$(PYTEST) tests/ -v

test-smoke: venv ## Run only health + registration tests (no deployment)
	$(PYTEST) tests/ -v -m "not deployment"

e2e: up test ## Start everything and run the full E2E suite

# ── Logs / status ─────────────────────────────────────────────────────────────

logs-root: ## Tail root orchestrator logs
	$(ROOT_COMPOSE) logs -f --tail=50

logs-cluster: ## Tail cluster orchestrator logs
	$(CLUSTER_COMPOSE) logs -f --tail=50

log: ## Follow a single container: make log s=system_manager
	@[ -n "$(s)" ] || (echo "Usage: make log s=<container_name>"; exit 1)
	docker logs -f --tail=100 $(s)

status: ## Show running Oakestra containers and their ports
	@docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" \
	    | grep -E "^NAMES|system_manager|cluster_manager|root_|cluster_|mongo|redis|mqtt|scheduler|abstractor|jwt|worker"

# ── Rebuild after code changes ────────────────────────────────────────────────

rebuild: ## Rebuild + restart a root service: make rebuild s=system_manager
	@[ -n "$(s)" ] || (echo "Usage: make rebuild s=<service>"; exit 1)
	$(ROOT_COMPOSE) build $(s)
	$(ROOT_COMPOSE) up -d $(s)

rebuild-cluster: ## Rebuild + restart a cluster service: make rebuild-cluster s=cluster_manager
	@[ -n "$(s)" ] || (echo "Usage: make rebuild-cluster s=<service>"; exit 1)
	$(CLUSTER_COMPOSE) build $(s)
	$(CLUSTER_COMPOSE) up -d $(s)

rebuild-scheduler: ## Rebuild root + cluster scheduler (shared Go source)
	$(ROOT_COMPOSE) build root_scheduler && $(ROOT_COMPOSE) up -d root_scheduler
	$(CLUSTER_COMPOSE) build cluster_scheduler && $(CLUSTER_COMPOSE) up -d cluster_scheduler

restart: ## Restart a container without rebuild: make restart s=system_manager
	@[ -n "$(s)" ] || (echo "Usage: make restart s=<container_name>"; exit 1)
	docker restart $(s)

# ── Real worker node (OrbStack VM) ────────────────────────────────────────────
# The dockerized worker covers most testing. Use an OrbStack Linux VM when you
# need a real systemd-managed NodeEngine (e.g. testing the installer or
# host-level behavior). From inside an OrbStack VM, 'host.orb.internal'
# resolves to the Mac - no manual IP detection needed.

vm-create: ## One-time: create the OrbStack VM and install the released NodeEngine
	@orbctl info $(WORKER_VM) >/dev/null 2>&1 \
	    && echo "VM '$(WORKER_VM)' already exists, skipping create" \
	    || orbctl create ubuntu $(WORKER_VM)
	@echo "Installing dependencies..."
	@orb -m $(WORKER_VM) sudo apt-get install -y wget
	@echo "Installing NodeEngine (version: alpha = develop branch)..."
	@orb -m $(WORKER_VM) bash -c \
	    "mkdir -p /var/tmp/oak-install && cd /var/tmp/oak-install && curl -sfL https://raw.githubusercontent.com/oakestra/oakestra/develop/scripts/InstallOakestraWorker.sh \
	     | OAKESTRA_VERSION=alpha bash"
	@echo "Writing NodeEngine config..."
	@orb -m $(WORKER_VM) sudo mkdir -p /etc/oakestra
	@printf '{"conf_version":"1.0","cluster_address":"host.orb.internal","cluster_ssl":false,"cluster_port":10100,"app_logs":"/tmp","overlay_network":"disabled","public_ip":false,"overlay_network_port":0,"mqtt_cert_file":"","mqtt_key_file":"","addons":null,"virtualizations":[{"virutalizaiton_name":"containerd","virutalizaiton_runtime":"docker","virutalizaiton_active":true,"virutalizaiton_config":[]}],"csi_drivers":null}\n' \
	    | orb -m $(WORKER_VM) sudo bash -c 'cat > /etc/oakestra/conf.json'
	@echo ""
	@echo "Done. Run 'make vm-up' to start the worker."

vm-build: check-repo ## Cross-compile NodeEngine + nodeengined for the VM (output: build/)
	@echo "Building for linux/$(GOARCH)..."
	@cd $(OAKESTRA_REPO)/go_node_engine && \
	    CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build \
	        -o $(CURDIR)/build/NodeEngine_$(GOARCH) NodeEngine.go
	@cd $(OAKESTRA_REPO)/go_node_engine && \
	    CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build \
	        -o $(CURDIR)/build/nodeengined_$(GOARCH) internal/daemon/nodeengined.go
	@echo "Built: build/NodeEngine_$(GOARCH) + nodeengined_$(GOARCH)"

vm-create-local: vm-build ## One-time VM setup using local NodeEngine source
	@orbctl info $(WORKER_VM) >/dev/null 2>&1 \
	    && echo "VM '$(WORKER_VM)' already exists, skipping create" \
	    || orbctl create ubuntu $(WORKER_VM)
	@echo "Installing containerd..."
	@orb -m $(WORKER_VM) sudo apt-get install -y containerd
	@orb -m $(WORKER_VM) sudo bash -c '\
	    mkdir -p /etc/containerd && \
	    containerd config default > /etc/containerd/config.toml && \
	    systemctl restart containerd'
	@orb -m $(WORKER_VM) sudo mkdir -p /var/log/oakestra
	@echo "Copying binaries and service file..."
	@orbctl push $(WORKER_VM) build/NodeEngine_$(GOARCH) /tmp/NodeEngine
	@orbctl push $(WORKER_VM) build/nodeengined_$(GOARCH) /tmp/nodeengined
	@orbctl push $(WORKER_VM) $(OAKESTRA_REPO)/go_node_engine/nodeengine.service /tmp/nodeengine.service
	@orb -m $(WORKER_VM) sudo bash -c '\
	    mv /tmp/NodeEngine /bin/NodeEngine && chmod 755 /bin/NodeEngine && \
	    mv /tmp/nodeengined /bin/nodeengined && chmod 755 /bin/nodeengined && \
	    mv /tmp/nodeengine.service /etc/systemd/system/nodeengine.service && \
	    systemctl daemon-reload && systemctl enable nodeengine'
	@echo "Configuring cluster address..."
	@orb -m $(WORKER_VM) sudo bash -c '\
	    NodeEngine config default && \
	    NodeEngine config cluster host.orb.internal && \
	    NodeEngine config network off'
	@echo ""
	@echo "Done. Run 'make vm-up' to start."

vm-rebuild: vm-build ## Rebuild NodeEngine from local source and redeploy to the VM
	@orb -m $(WORKER_VM) sudo systemctl stop nodeengine 2>/dev/null || true
	@echo "Pushing updated binaries..."
	@orbctl push $(WORKER_VM) build/NodeEngine_$(GOARCH) /tmp/NodeEngine
	@orbctl push $(WORKER_VM) build/nodeengined_$(GOARCH) /tmp/nodeengined
	@orb -m $(WORKER_VM) sudo bash -c '\
	    mv /tmp/NodeEngine /bin/NodeEngine && chmod 755 /bin/NodeEngine && \
	    mv /tmp/nodeengined /bin/nodeengined && chmod 755 /bin/nodeengined'
	@orb -m $(WORKER_VM) sudo systemctl start nodeengine
	@echo "NodeEngine updated and restarted."

vm-up: ## Start NodeEngine inside the worker VM
	orb -m $(WORKER_VM) sudo systemctl start nodeengine
	@echo "NodeEngine started. Use 'make vm-logs' to follow output."

vm-down: ## Stop NodeEngine inside the worker VM
	orb -m $(WORKER_VM) sudo systemctl stop nodeengine

vm-logs: ## Tail NodeEngine logs from the worker VM
	orb -m $(WORKER_VM) sudo tail -f /var/log/oakestra/nodeengine.log

vm-shell: ## Open a shell in the worker VM
	orb shell $(WORKER_VM)

vm-delete: ## Delete the worker VM entirely (destructive)
	@printf "Delete VM '$(WORKER_VM)'? This cannot be undone. [y/N] " \
	    && read ans && [ "$${ans:-N}" = "y" ]
	orbctl delete $(WORKER_VM)

# ── Utilities ─────────────────────────────────────────────────────────────────

open: ## Open the API docs in the browser
	open "http://localhost:10000/api/docs"

clean: ## Remove containers AND data volumes - fresh start
	@printf "This deletes all Oakestra volumes (MongoDB, Redis). Continue? [y/N] " \
	    && read ans && [ "$${ans:-N}" = "y" ]
	$(WORKER_COMPOSE) down -v
	$(CLUSTER_COMPOSE) down -v
	$(ROOT_COMPOSE) down -v
