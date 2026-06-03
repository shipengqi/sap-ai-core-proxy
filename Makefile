.PHONY: build run test vet clean

build:
	go build -o sap-ai-core-proxy ./...

run:
	go run ./...

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f sap-ai-core-proxy
