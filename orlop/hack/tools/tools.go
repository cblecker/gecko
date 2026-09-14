//go:build tools

// Package tools pins tool binaries as import side-effects so that
// "go mod tidy" keeps their transitive dependencies in go.sum and
// "go build -tags=tools" can produce a reproducible binary.
package tools

import (
	_ "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
	_ "sigs.k8s.io/kube-api-linter/pkg/plugin"
)
