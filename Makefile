# Formae Plugin Makefile
#
# Targets:
#   build   - Build the plugin binary
#   test    - Run tests
#   lint    - Run linter
#   clean   - Remove build artifacts
#   install - Build and install plugin locally (binary + schema + manifest)

# Plugin metadata - extracted from formae-plugin.pkl
PLUGIN_NAME := $(shell pkl eval -x 'name' formae-plugin.pkl 2>/dev/null || echo "example")
PLUGIN_VERSION := $(shell pkl eval -x 'version' formae-plugin.pkl 2>/dev/null || echo "0.0.0")
PLUGIN_NAMESPACE := $(shell pkl eval -x 'namespace' formae-plugin.pkl 2>/dev/null || echo "EXAMPLE")

# Build settings
GO := go
GOFLAGS := -trimpath
BINARY := $(PLUGIN_NAME)

# Installation paths
# NOTE: Directory structure will change from <namespace> to <name> in a future version
PLUGIN_BASE_DIR := $(HOME)/.pel/formae/plugins
INSTALL_DIR := $(PLUGIN_BASE_DIR)/$(PLUGIN_NAME)/v$(PLUGIN_VERSION)

.PHONY: all build test test-unit test-integration lint verify-schema clean install help setup-credentials clean-environment conformance-test conformance-test-crud conformance-test-discovery

all: build

## build: Build the plugin binary
build:
	$(GO) build $(GOFLAGS) -o bin/$(BINARY) .

## test: Run all tests
test:
	$(GO) test -v ./...

## test-unit: Run unit tests only (tests with //go:build unit tag)
test-unit:
	$(GO) test -v -tags=unit ./...

## test-integration: Run integration tests (requires cloud credentials)
## Add tests with //go:build integration tag
test-integration:
	$(GO) test -v -tags=integration ./...

## lint: Run golangci-lint
lint:
	golangci-lint run

## verify-schema: Validate PKL schema files
## Checks that schema files are well-formed and follow formae conventions.
verify-schema:
	$(GO) run github.com/platform-engineering-labs/formae/pkg/plugin/testutil/cmd/verify-schema --namespace $(PLUGIN_NAMESPACE) ./schema/pkl

## clean: Remove build artifacts
clean:
	rm -rf bin/ dist/

## install: Build and install plugin locally (binary + schema + manifest)
## Installs to ~/.pel/formae/plugins/<namespace>/v<version>/
## Removes any existing versions of the plugin first to ensure clean state.
install: build
	@echo "Installing $(PLUGIN_NAME) v$(PLUGIN_VERSION) (namespace: $(PLUGIN_NAMESPACE))..."
	@rm -rf $(PLUGIN_BASE_DIR)/$(PLUGIN_NAME)
	@mkdir -p $(INSTALL_DIR)/schema/pkl
	@cp bin/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@cp -r schema/pkl/* $(INSTALL_DIR)/schema/pkl/
	@cp formae-plugin.pkl $(INSTALL_DIR)/
	@echo "Installed to $(INSTALL_DIR)"
	@echo "  - Binary: $(INSTALL_DIR)/$(BINARY)"
	@echo "  - Schema: $(INSTALL_DIR)/schema/pkl/"
	@echo "  - Manifest: $(INSTALL_DIR)/formae-plugin.pkl"

## help: Show this help message
help:
	@echo "Available targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## setup-credentials: Provision cloud provider credentials
## Edit scripts/ci/setup-credentials.sh to configure for your provider.
setup-credentials:
	@./scripts/ci/setup-credentials.sh

## clean-environment: Clean up test resources in cloud environment
## Called before and after conformance tests. Edit scripts/ci/clean-environment.sh
## to configure for your provider.
clean-environment:
	@./scripts/ci/clean-environment.sh

## conformance-test: Run all conformance tests (CRUD + discovery)
## Usage: make conformance-test [VERSION=0.80.0] [TEST=s3-bucket] [PARALLEL=10] [TIMEOUT=15]
## Downloads the specified formae version (or latest) and runs conformance tests.
## Calls setup-credentials and clean-environment automatically.
##
## Parameters:
##   VERSION  - Formae version to test against (default: latest)
##   TEST     - Filter tests by name pattern (e.g., TEST=s3-bucket)
##   PARALLEL - Max parallel tests (default: 1 = sequential)
##   TIMEOUT  - Timeout in minutes for long-running operations (default: 5)
conformance-test: install setup-credentials
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-crud-run conformance-test-discovery-run VERSION=$(VERSION) TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-crud: Run CRUD tests with cleanup (convenience for local dev)
conformance-test-crud: install setup-credentials
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-crud-run VERSION=$(VERSION) TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-discovery: Run discovery tests with cleanup (convenience for local dev)
conformance-test-discovery: install setup-credentials
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-discovery-run VERSION=$(VERSION) TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-crud-run: Run only CRUD lifecycle tests (no cleanup)
## Used by CI matrix jobs where cleanup is managed separately.
conformance-test-crud-run:
	@echo "Running CRUD conformance tests..."
	@FORMAE_TEST_FILTER="$(TEST)" FORMAE_TEST_TYPE=crud FORMAE_TEST_PARALLEL="$(PARALLEL)" FORMAE_TEST_TIMEOUT="$(TIMEOUT)" ./scripts/run-conformance-tests.sh $(VERSION)

## conformance-test-discovery-run: Run only discovery tests (no cleanup)
## Used by CI matrix jobs where cleanup is managed separately.
conformance-test-discovery-run:
	@echo "Running discovery conformance tests..."
	@FORMAE_TEST_FILTER="$(TEST)" FORMAE_TEST_TYPE=discovery FORMAE_TEST_PARALLEL="$(PARALLEL)" FORMAE_TEST_TIMEOUT="$(TIMEOUT)" ./scripts/run-conformance-tests.sh $(VERSION)
