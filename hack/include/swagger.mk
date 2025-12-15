# ==============================================================================
# Makefile helper functions for swagger
#

swagger.run: tools.verify.swagger
	@echo "===========> Generating swagger API docs"
	@swagger mixin `find $(REPO_ROOT)/api/openapi -name "*.swagger.json"` \
		-q                                                    \
		--keep-spec-order                                     \
		--format=yaml                                         \
		--ignore-conflicts                                    \
		-o $(REPO_ROOT)/api/openapi/apiserver/v1/openapi.yaml
	@echo "Generated at: $(REPO_ROOT)/api/openapi/apiserver/v1/openapi.yaml"

swagger.serve: tools.verify.swagger
	@swagger serve -F=redoc --no-open --port 65534 $(REPO_ROOT)/api/openapi/apiserver/v1/openapi.yaml

.PHONY: swagger.run swagger.serve