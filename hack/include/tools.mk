TOOLS ?= gsemver golangci-lint goimports releaser ginkgo protoc-plugins protoc-go-inject-tag protolint swagger

tools.verify: $(addprefix tools.verify., $(TOOLS))

tools.install: $(addprefix tools.install., $(TOOLS))

tools.verify.%:
	@if ! which $* &>/dev/null; then $(MAKE) tools.install.$*; fi

tools.install.%:
	@echo "===========> Installing $*"
	@$(MAKE) install.$*

install.golangci-lint:
	@$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

install.goimports:
	@$(GO) install golang.org/x/tools/cmd/goimports@latest

install.gsemver:
	@$(GO) install github.com/arnaud-deprez/gsemver@latest

install.releaser:
	@$(GO) install github.com/goreleaser/goreleaser@latest

install.ginkgo:
	@$(GO) install github.com/onsi/ginkgo/v2/ginkgo@latest

install.protoc-plugins:
	@$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@v1.35.2
	@$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
	@$(GO) install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.24.0
	@$(GO) install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@v2.24.0	

install.protoc-go-inject-tag:
	@$(GO) install github.com/favadi/protoc-go-inject-tag@latest

install.protolint:
	@$(GO) install github.com/yoheimuta/protolint/cmd/protolint@latest

install.swagger:
	@$(GO) install github.com/go-swagger/go-swagger/cmd/swagger@latest

.PHONY: tools.verify tools.install tools.install.% tools.verify.% install.golangci-lint \
	install.goimports install.protoc-plugins install.swagger \
	install.protoc-go-inject-tag install.protolint