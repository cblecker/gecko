.PHONY: build test

BINARY_NAME=marketplace-signup

build:
	go build -o bin/$(BINARY_NAME) ./cmd/server

test:
	go test ./...

include ../hack/lint.mk
