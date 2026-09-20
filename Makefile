.PHONY: build test format

build:
	go build -trimpath -o bin/hex ./cmd/hex

test:
	go test -race ./...
	go vet ./...

format:
	gofmt -w cmd internal server examples
