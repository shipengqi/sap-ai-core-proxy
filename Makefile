.PHONY: help build run test vet lint docker.build clean

help: ## Show this help
	@grep -E '^[a-zA-Z_.]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary
	go build -o sap-ai-core-proxy ./...

run: ## Run the server
	go run ./...

test: ## Run all tests
	go test ./...

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

docker.build: build ## Build Docker image (usage: make docker.build TAG=v1.0.0)
	docker build -t ghcr.io/shipengqi/sap-ai-core-proxy:$(or $(TAG),dev) .

clean: ## Remove build artifacts
	rm -f sap-ai-core-proxy
