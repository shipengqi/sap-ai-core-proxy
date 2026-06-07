.PHONY: help build run test vet lint docker.build clean deploy

INSTALL_BIN  := /usr/local/bin/aicoreproxy
INSTALL_CFG  := /etc/aicoreproxy
SERVICE_FILE := /etc/systemd/system/aicoreproxy.service

help: ## Show this help
	@grep -E '^[a-zA-Z_.]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary
	go build -o aicoreproxy .

run: ## Run the server
	go run ./...

test: ## Run all tests
	go test ./...

test.integration: ## Run integration tests (mock mode if SAP_AI_CORE_* env vars not set)
	go test -v -timeout 120s ./test/...

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

docker.build: build ## Build Docker images for ghcr.io and Alibaba Cloud ACR (usage: make docker.build TAG=v1.0.0)
	$(eval ARCH := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/'))
	docker build --platform linux/$(ARCH) \
		-t ghcr.io/shipengqi/sap-ai-core-proxy:$(or $(TAG),dev)-$(ARCH) \
		-t registry.cn-hangzhou.aliyuncs.com/myaii/sap-ai-core-proxy:$(or $(TAG),dev)-$(ARCH) \
		.

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
