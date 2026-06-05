.PHONY: help build run test vet lint docker.build clean

help: ## Show this help
	@grep -E '^[a-zA-Z_.]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary
	go build -o sap-ai-core-proxy .

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

docker.build: build ## Build Docker images for ghcr.io and Alibaba Cloud ACR (usage: make docker.build TAG=v1.0.0 ACR_REGISTRY=<your-registry>)
	$(eval ARCH := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/'))
	docker build -t ghcr.io/shipengqi/sap-ai-core-proxy:$(or $(TAG),dev)-$(ARCH) .
	docker build -t registry.cn-hangzhou.aliyuncs.com/myaii/sap-ai-core-proxy:$(or $(TAG),dev)-$(ARCH) .


clean: ## Remove build artifacts
	rm -f sap-ai-core-proxy
