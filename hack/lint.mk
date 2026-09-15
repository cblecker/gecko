# Resolve paths before any other Makefiles are included.
TOOLS_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/tools)
TOOLS_MOD := $(TOOLS_DIR)/go.mod
KUBEAPILINTER_PLUGIN := $(TOOLS_DIR)/bin/kube-api-linter.so

# Use identical dependencies, CGO, and inherited GOFLAGS for both builds.
# Keep the application working directory so the linter loads its module.
LINT_GO := CGO_ENABLED=1 go
# Use classic relocations to avoid plugin load failures with the macOS 27 linker.
# Apply the same C linker flags to the plugin and its host executable.
ifeq ($(shell go env GOOS),darwin)
LINT_GO := CGO_ENABLED=1 CGO_LDFLAGS="$(shell go env CGO_LDFLAGS) -Wl,-no_fixup_chains" go
endif
GOLANGCI_LINT := $(LINT_GO) tool -modfile=$(TOOLS_MOD) golangci-lint

.PHONY: lint lint-fix lint-fmt
lint: lint-plugin
	$(GOLANGCI_LINT) run --config ./.golangci.yml --modules-download-mode=readonly -v

lint-fix: lint-plugin
	$(GOLANGCI_LINT) run --config ./.golangci.yml --fix -v

lint-fmt:
	$(GOLANGCI_LINT) fmt --config ./.golangci.yml

# Always invoke the build; Go's cache handles dependency and toolchain changes.
.PHONY: lint-plugin
lint-plugin:
	@mkdir -p $(TOOLS_DIR)/bin
	$(LINT_GO) build -modfile=$(TOOLS_MOD) -buildmode=plugin -o $(KUBEAPILINTER_PLUGIN) sigs.k8s.io/kube-api-linter/pkg/plugin
