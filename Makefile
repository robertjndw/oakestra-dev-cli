# ─── Oakestra local testing on macOS ─────────────────────────────────────────
# Everything about running, editing and testing the stack lives in the oak-dev
# CLI - `oak-dev --help` is the source of truth. This Makefile only covers the
# two things oak-dev can't do for itself: installing oak-dev, and driving the
# OrbStack VM used as a *real* (systemd-managed) worker node.
#
# Quickstart:
#   make install                          # puts oak-dev on your PATH
#   cp .env.example .env                  # once, edit as needed
#   cp oak-dev.yaml.example oak-dev.yaml  # once, pick live: components + stack
#   oak-dev doctor --fix                  # prerequisites + bootstrap
#   oak-dev dev                           # start everything, watch, stream logs
# ─────────────────────────────────────────────────────────────────────────────

# Load local overrides (.env is gitignored) - oak-dev reads this itself, but the
# vm-* targets below shell out to orbctl directly and need it here too.
-include .env

OAKESTRA_REPO ?= ../oakestra
override OAKESTRA_REPO := $(abspath $(OAKESTRA_REPO))
export OAKESTRA_REPO

# Where `make install` puts the binary. Override if you keep tools elsewhere:
#   make install PREFIX=/usr/local/bin
PREFIX ?= $(HOME)/.local/bin

# Name of the OrbStack Linux VM used as a real worker node (override via .env)
WORKER_VM ?= oak-worker

# Translate Mac arch to Go/Linux arch (arm64 stays arm64; x86_64 → amd64)
GOARCH := $(shell uname -m | sed 's/x86_64/amd64/')

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help install build \
        vm-build vm-create vm-create-local vm-rebuild vm-up vm-down \
        vm-logs vm-shell vm-delete

help: ## Show this help (everything else: oak-dev --help)
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
	    | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  Everything else is oak-dev: run 'oak-dev --help'."

# ── Installing oak-dev ────────────────────────────────────────────────────────

install: ## Build oak-dev and put it on your PATH (PREFIX=~/.local/bin)
	@mkdir -p $(PREFIX)
	go build -ldflags "-X main.version=$(VERSION)" -o $(PREFIX)/oak-dev ./cmd/oak-dev
	@echo "Installed $(PREFIX)/oak-dev ($(VERSION))"
	@command -v oak-dev >/dev/null 2>&1 \
	    || echo "NOTE: $(PREFIX) is not on your PATH - add it to use 'oak-dev' directly."
	@echo "Shell completion: oak-dev completion --help"
	@echo "AI agent skill:   oak-dev skill install --help"

build: ## Build oak-dev into ./bin without installing it
	go build -ldflags "-X main.version=$(VERSION)" -o bin/oak-dev ./cmd/oak-dev
	@echo "Built bin/oak-dev ($(VERSION))"

# ── Real worker node (OrbStack VM) ────────────────────────────────────────────
# Kept as raw Make, unchanged: macOS-only, for testing the real installer and
# systemd units. Not part of the oak-dev rewrite - see "explicitly out of
# scope" in the design doc.

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

vm-build: ## Cross-compile NodeEngine + nodeengined for the VM (output: build/)
	@[ -f "$(OAKESTRA_REPO)/version.txt" ] \
	    || (echo "ERROR: no oakestra checkout at $(OAKESTRA_REPO) - set OAKESTRA_REPO in .env"; exit 1)
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


