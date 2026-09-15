.PHONY: build test

BINARY_NAME=gecko-controllers

build:
	go build -o bin/$(BINARY_NAME) .

test:
	go test ./...

include ../hack/lint.mk
