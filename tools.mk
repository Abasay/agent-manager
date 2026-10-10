# Pinned Go dev tools for this repo, shared by local dev and CI. `make tools`
# installs them into .tools/bin, and every make recipe sees that directory
# first on PATH. CI installs only the group a job needs.
# Cluster CLIs (k3d, kubectl, helm) and Docker are prerequisites you install yourself.

TOOLS_ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
TOOLS_BIN  := $(TOOLS_ROOT)/.tools/bin

export PATH := $(TOOLS_BIN):$(PATH)

# GOTOOLCHAIN=auto lets a tool that needs a newer Go than the job's (cli is on 1.25)
# fetch that toolchain for its own build; it doesn't change how the repo builds.
# Go only switches toolchains in module mode, and CI runners set GO111MODULE=auto
# while the repo root has no go.mod, so module mode is forced here.
GO_INSTALL = GOBIN=$(TOOLS_BIN) GOTOOLCHAIN=auto GO111MODULE=on go install

# In GitHub Actions, later steps outside make (lint actions, plain run steps) need the tools too.
TOOLS_CI_PATH = @[ -z "$$GITHUB_PATH" ] || echo "$(TOOLS_BIN)" >> "$$GITHUB_PATH"

# Keep the including Makefile's default goal when this file comes first.
_tools_saved_default_goal := $(.DEFAULT_GOAL)

.PHONY: tools tools-codegen tools-lint tools-dev
tools: tools-codegen tools-lint tools-dev

# Code generation and formatting (output is committed and diffed in CI)
tools-codegen:
	$(GO_INSTALL) github.com/google/wire/cmd/wire@v0.7.0
	$(GO_INSTALL) github.com/matryer/moq@v0.5.3
	$(GO_INSTALL) github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.6.0
	$(GO_INSTALL) golang.org/x/tools/cmd/goimports@v0.51.0
	$(GO_INSTALL) mvdan.cc/gofumpt@v0.12.0
	$(TOOLS_CI_PATH)

tools-lint:
	$(GO_INSTALL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
	$(GO_INSTALL) honnef.co/go/tools/cmd/staticcheck@2026.1
	$(TOOLS_CI_PATH)

tools-dev:
	$(GO_INSTALL) github.com/air-verse/air@v1.67.4
	# ginkgo must match github.com/onsi/ginkgo/v2 in test/e2e/go.mod
	$(GO_INSTALL) github.com/onsi/ginkgo/v2/ginkgo@v2.28.3
	$(GO_INSTALL) github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@v0.162.0
	$(TOOLS_CI_PATH)

.DEFAULT_GOAL := $(_tools_saved_default_goal)
