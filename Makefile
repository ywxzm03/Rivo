SHELL := bash
.DELETE_ON_ERROR:
.DEFAULT_GOAL := help
.SHELLFLAGS := -eu -o pipefail -c
MAKEFLAGS += --warn-undefined-variables
MAKEFLAGS += --no-builtin-rules
MAKEFLAGS += --no-print-directory

GO ?= go
TOOLS_DIR ?= .tools
TOOLS_BIN := $(TOOLS_DIR)/bin
COVERAGE_FILE ?= coverage.txt

GOLANGCI_LINT_VERSION ?= v2.15.0
GOIMPORTS_VERSION ?= v0.52.0
MODERNIZE_VERSION ?= v0.21.1
DEADCODE_VERSION ?= v0.52.0

ifeq ($(OS),Windows_NT)
BIN_EXT := .exe
else
BIN_EXT :=
endif

GOLANGCI_LINT := $(TOOLS_BIN)/golangci-lint$(BIN_EXT)
GOIMPORTS := $(TOOLS_BIN)/goimports$(BIN_EXT)
MODERNIZE := $(TOOLS_BIN)/modernize$(BIN_EXT)
DEADCODE := $(TOOLS_BIN)/deadcode$(BIN_EXT)

COVERAGE_MIN ?= 80

.PHONY: help build test test-race cover cover-check vet fmt check-fmt lint deadcode tools clean

help: ## Show available commands
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build all packages
	$(GO) build ./...

test: ## Run all tests
	$(GO) test ./...

test-race: ## Run tests with race detector
	$(GO) test -race ./...

cover: ## Run tests and write coverage profile
	$(GO) test ./... -coverprofile=$(CURDIR)/$(COVERAGE_FILE) -covermode=atomic

cover-check: cover ## Enforce coverage threshold on the whole repository
	@coverage=$$($(GO) tool cover -func=$(COVERAGE_FILE) | awk '$$1 == "total:" {gsub("%", "", $$NF); print $$NF}'); \
	awk -v c="$$coverage" -v min="$(COVERAGE_MIN)" 'BEGIN { \
		if (c < min) { printf "coverage %.1f%% < %d%%\n", c, min; exit 1 } \
		printf "coverage %.1f%% >= %d%%\n", c, min \
	}'

vet: ## Run go vet
	$(GO) vet ./...

fmt: $(MODERNIZE) $(GOIMPORTS) ## Format code, rewrite imports, modernize syntax
	@# modernize 分多轮应用修复，首轮必有未应用项而返回非零，跑到收敛为止
	@for i in 1 2 3 4 5; do \
		out="$$($(MODERNIZE) -fix -test ./... 2>&1)"; \
		echo "$$out"; \
		case "$$out" in *"applied"*) continue ;; *) break ;; esac; \
	done
	$(GO) fmt ./...
	$(GOIMPORTS) -w .

check-fmt: $(GOIMPORTS) ## Verify formatting without modifying files
	@unformatted="$$(git ls-files '*.go' | xargs gofmt -l)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt required:"; echo "$$unformatted"; \
		echo "Run 'make fmt' and commit."; exit 1; \
	fi
	@changed="$$($(GOIMPORTS) -l .)"; \
	if [ -n "$$changed" ]; then \
		echo "goimports required:"; echo "$$changed"; \
		echo "Run 'make fmt' and commit."; exit 1; \
	fi

lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run ./... --timeout=10m

deadcode: $(DEADCODE) ## Report unreachable functions (must be empty)
	$(DEADCODE) -test ./...

tools: $(GOLANGCI_LINT) $(GOIMPORTS) $(MODERNIZE) $(DEADCODE) ## Install pinned dev tools into .tools/bin

clean: ## Remove coverage artifacts
	rm -f $(COVERAGE_FILE)

$(TOOLS_BIN):
	mkdir -p $@

$(GOLANGCI_LINT): | $(TOOLS_BIN)
	GOBIN=$(abspath $(TOOLS_BIN)) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOIMPORTS): | $(TOOLS_BIN)
	GOBIN=$(abspath $(TOOLS_BIN)) $(GO) install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

$(MODERNIZE): | $(TOOLS_BIN)
	GOBIN=$(abspath $(TOOLS_BIN)) $(GO) install golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@$(MODERNIZE_VERSION)

$(DEADCODE): | $(TOOLS_BIN)
	GOBIN=$(abspath $(TOOLS_BIN)) $(GO) install golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION)
