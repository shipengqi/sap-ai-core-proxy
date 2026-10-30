.PHONY: help build run tui server test vet lint docker.build clean deploy

# make run starts the proxy with an in-process TUI on interactive terminals.
# Use WITH_TUI=0 for server-only mode.
WITH_TUI ?= 1

INSTALL_BIN  := /usr/local/bin/aicoreproxy
INSTALL_CFG  := /etc/aicoreproxy
SERVICE_FILE := /etc/systemd/system/aicoreproxy.service

# Build identity, stamped into internal/version so that `aicoreproxy -version`,
# GET /version, the dashboards and the startup log all report the same thing as
# the tag that was released. VERSION is the file the bump-version workflow
# writes; COMMIT and BUILD_DATE describe the tree the binary came from, which is
# what actually distinguishes two builds of the same version.
MODULE     := github.com/shipengqi/sap-ai-core-proxy
VERSION    ?= $(shell cat VERSION 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.version=$(VERSION) \
	-X $(MODULE)/internal/version.commit=$(COMMIT) \
	-X $(MODULE)/internal/version.buildDate=$(BUILD_DATE)

help: ## Show this help
	@grep -E '^[a-zA-Z_.]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary
	go build -ldflags "$(LDFLAGS)" -o aicoreproxy .

run: ## Run proxy and TUI (use WITH_TUI=0 for proxy only)
	go run . $(if $(filter 0,$(WITH_TUI)),--no-tui,)

server: ## Run proxy only
	go run . serve --no-tui

tui: ## Run the terminal dashboard against an existing proxy
	go run . tui

test: ## Run all tests
	go test ./...

test.integration: ## Run integration tests (mock mode if SAP_AI_CORE_* env vars not set)
	go test -v -timeout 120s ./test/...

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

docker.build: ## Build Docker images for ghcr.io and Alibaba Cloud ACR (usage: make docker.build TAG=v1.0.0)
	$(eval ARCH := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/'))
	go build -ldflags "$(LDFLAGS)" -o sap-ai-core-proxy .
	docker build --platform linux/$(ARCH) \
		-t ghcr.io/shipengqi/sap-ai-core-proxy:$(or $(TAG),dev)-$(ARCH) \
		-t registry.cn-hangzhou.aliyuncs.com/myaii/sap-ai-core-proxy:$(or $(TAG),dev)-$(ARCH) \
		.
	rm -f sap-ai-core-proxy

deploy: build ## Install or update aicoreproxy as a systemd service (run as root: sudo make deploy)
	@echo "==> Installing binary"
	install -m 755 aicoreproxy $(INSTALL_BIN)

	@echo "==> Creating user/group"
	getent group aicoreproxy  >/dev/null || groupadd -r aicoreproxy
	getent passwd aicoreproxy >/dev/null || useradd -r -g aicoreproxy -s /sbin/nologin -M -c "SAP AI Core Proxy" aicoreproxy

	@echo "==> Installing config directory"
	install -d -m 750 -o root -g aicoreproxy $(INSTALL_CFG)
	@if [ ! -f $(INSTALL_CFG)/config.json ]; then \
		install -m 640 -o root -g aicoreproxy config.json.example $(INSTALL_CFG)/config.json; \
		echo ""; \
		echo "  *** Edit $(INSTALL_CFG)/config.json with your SAP AI Core credentials ***"; \
		echo ""; \
	fi

	@echo "==> Installing systemd service"
	install -m 644 deploy/aicoreproxy.service $(SERVICE_FILE)
	systemctl daemon-reload
	systemctl enable aicoreproxy

	@echo "==> Restarting service"
	systemctl restart aicoreproxy
	systemctl status aicoreproxy --no-pager

clean: ## Remove build artifacts
	rm -f aicoreproxy
