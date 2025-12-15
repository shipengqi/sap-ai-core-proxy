GO := go

# GO_SUPPORTED_VERSIONS ?= 1.20|1.21|1.22|1.23|1.24|1.25

# .PHONY: go.build.verify
# go.build.verify:
# ifneq ($(shell go version | grep -q -E '\bgo($(GO_SUPPORTED_VERSIONS))\b' && echo 0 || echo 1), 0)
# 	$(error unsupported go version. Please install one of the following supported version: '$(GO_SUPPORTED_VERSIONS)')
# endif

GO_BUILD_FLAGS += -ldflags "$(GO_LDFLAGS)"

ifeq ($(GOOS),windows)
	GO_OUT_EXT := .exe
endif

go.build.verify:
	@if ! which go &>/dev/null; then echo "Cannot found go compile tool. Please install go tool first."; exit 1; fi

.PHONY: go.build.dirs
go.build.dirs:
	@mkdir -p $(OUTPUT_DIR)

.PHONY: go.build
go.build: go.build.verify go.build.dirs
	@echo "===========> Building: $(OUTPUT_DIR)/$(BIN)"
	@cd $(REPO_ROOT)/as-api && GOOS=$(GOOS) \
		PKG=$(PKG) BIN=$(BIN) \
		OUTPUT_DIR=$(OUTPUT_DIR) \
		GO_LDFLAGS="$(GO_LDFLAGS)" \
		bash $(REPO_ROOT)/hack/build.sh

go.format: tools.verify.goimports ## 格式化 Go 源码.
	@echo "===========> Running formaters to format codes"
	@$(FIND) -type f -name '*.go' | $(XARGS) gofmt -s -w
	@$(FIND) -type f -name '*.go' | $(XARGS) goimports -w -local $(ROOT_PACKAGE)
	@$(GO) mod edit -fmt

go.tidy: ## 自动添加/移除依赖包.
	@echo "===========> Running 'go mod tidy' ..."
	@$(GO) mod tidy

go.test: ## 执行单元测试.
	@echo "===========> Running unit tests"
	@mkdir -p $(OUTPUT_DIR)
	@$(GO) test -race -cover \
		-coverprofile=$(OUTPUT_DIR)/coverage.out \
		-timeout=10m -shuffle=on -short \
		-v `go list ./...|egrep -v 'tools|vendor|third_party'`

go.cover: go.test
	@echo "===========> Running code coverage tests"
	@$(GO) tool cover -func=$(OUTPUT_DIR)/coverage.out | awk -v target=$(COVERAGE) -f $(REPO_ROOT)/hack/coverage.awk

go.lint: tools.verify.golangci-lint
	@echo "===========> Running golangci-lint to lint source codes"
	@golangci-lint run -c $(REPO_ROOT)/.golangci.yaml $(REPO_ROOT)/...


.PHONY: go.build.verify go.build.% go.build go.format go.tidy go.test go.cover go.lint