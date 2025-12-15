gen.protoc: tools.verify.protoc-gen-go ## 编译 protobuf 文件.
	@echo "===========> Generate protobuf files"
	@mkdir -p $(PROJ_ROOT_DIR)/api/openapi
	@# --grpc-gateway_out 用来在 pkg/api/apiserver/v1/ 目录下生成反向服务器代码 apiserver.pb.gw.go
	@# --openapiv2_out 用来在 api/openapi/apiserver/v1/ 目录下生成 Swagger V2 接口文档
	@protoc                                            \
		--proto_path=$(APIROOT)                          \
		--proto_path=$(PROJ_ROOT_DIR)/third_party/protobuf             \
		--go_out=paths=source_relative:$(APIROOT)        \
		--go-grpc_out=paths=source_relative:$(APIROOT)   \
		--grpc-gateway_out=allow_delete_body=true,paths=source_relative:$(APIROOT) \
		--openapiv2_out=$(PROJ_ROOT_DIR)/api/openapi \
		--openapiv2_opt=allow_delete_body=true,logtostderr=true \
		--defaults_out=paths=source_relative:$(APIROOT) \
		$(shell find $(APIROOT) -name *.proto)
	@find . -name "*.pb.go" -exec protoc-go-inject-tag -input={} \;
	
.PHONY: gen.protoc