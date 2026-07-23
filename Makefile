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

.PHONY: all build test test-unit test-integration lint verify-schema verify-examples clean install help setup-credentials clean-environment conformance-test conformance-test-crud conformance-test-discovery conformance-test-crud-run conformance-test-discovery-run

all: build

## build: Build the plugin binary and update manifest
build:
	@mkdir -p schema/pkl && echo "$(PLUGIN_VERSION)" > schema/pkl/VERSION
	$(GO) build $(GOFLAGS) -o bin/$(BINARY) .
	@SDK_MIN=$$($(GO) list -m -f '{{.Dir}}' github.com/platform-engineering-labs/formae/pkg/plugin 2>/dev/null | xargs -I{} grep 'MinFormaeVersion' {}/version.go 2>/dev/null | grep -oE '"[0-9]+\.[0-9]+\.[0-9]+"' | tr -d '"'); \
	DECLARED=$$(pkl eval -x minFormaeVersion formae-plugin.pkl 2>/dev/null); \
	EFFECTIVE=$$(printf '%s\n%s\n' "$$SDK_MIN" "$$DECLARED" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$$' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1); \
	if [ -n "$$EFFECTIVE" ] && [ "$$EFFECTIVE" != "$$DECLARED" ]; then \
		echo "Raising minFormaeVersion to $$EFFECTIVE (sdk=$$SDK_MIN, declared=$$DECLARED)"; \
		if [ "$$(uname)" = "Darwin" ]; then \
			sed -i '' 's/^minFormaeVersion = .*/minFormaeVersion = "'"$$EFFECTIVE"'"/' formae-plugin.pkl; \
		else \
			sed -i 's/^minFormaeVersion = .*/minFormaeVersion = "'"$$EFFECTIVE"'"/' formae-plugin.pkl; \
		fi; \
	else \
		echo "Keeping declared minFormaeVersion=$$DECLARED (sdk=$$SDK_MIN, never downgrade below declared)"; \
	fi

## test: Run all tests
test:
	$(GO) test -v ./...

## test-unit: Run unit tests only (tests with //go:build unit tag)
test-unit:
	$(GO) test -v -tags=unit ./...

## test-integration: Run integration tests (requires cloud credentials)
## Add tests with //go:build integration tag
test-integration:
	$(GO) test -v -tags=integration -timeout 30m ./...

## lint: Run golangci-lint
lint:
	golangci-lint run

## verify-schema: Validate PKL schema files
## Checks that schema files are well-formed and follow formae conventions.
verify-schema:
	$(GO) run github.com/platform-engineering-labs/formae/pkg/plugin/testutil/cmd/verify-schema --namespace $(PLUGIN_NAMESPACE) ./schema/pkl

## verify-examples: Validate that every CloudFront/ACM example .pkl file evaluates cleanly
## Catches Resolvable shape regressions and cross-resource type-mismatches
## without requiring deploy.
verify-examples:
	@if [ ! -f examples/PklProject.deps.json ]; then \
		echo "Resolving examples/PklProject dependencies..."; \
		(cd examples && pkl project resolve); \
	fi
	@for f in $$(find examples/cloudfront-* -name '*.pkl' 2>/dev/null); do \
		echo "Evaluating $$f..."; \
		FORMAE_TEST_RUN_ID=verify pkl eval --project-dir examples "$$f" >/dev/null || exit 1; \
	done
	@echo "All examples evaluated cleanly."

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
	@if [ -f schema/Config.pkl ]; then cp schema/Config.pkl $(INSTALL_DIR)/schema/; fi
	@cp formae-plugin.pkl $(INSTALL_DIR)/
	@echo "Installed to $(INSTALL_DIR)"
	@echo "  - Binary: $(INSTALL_DIR)/$(BINARY)"
	@echo "  - Schema: $(INSTALL_DIR)/schema/"
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
## Usage: make conformance-test [TEST=s3-bucket] [PARALLEL=1] [TIMEOUT=60]
## Calls clean-environment before and after tests.
##
## Parameters:
##   TEST     - Filter tests by name pattern (e.g., TEST=s3-bucket)
##   PARALLEL - Concurrent tests inside the SDK (default: 1 = sequential)
##   TIMEOUT  - Test timeout in minutes (default: 60)
##
## The conformance SDK installs the latest released formae via orbital
## unless FORMAE_BINARY is set (e.g. by nightly which builds from source).
conformance-test: install
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-crud-run conformance-test-discovery-run TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-crud: Run CRUD tests with cleanup (convenience for local dev)
conformance-test-crud: install
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-crud-run TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-discovery: Run discovery tests with cleanup (convenience for local dev)
conformance-test-discovery: install
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-discovery-run TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-crud-run: Run only CRUD lifecycle tests (no cleanup)
## Used by CI matrix jobs where cleanup is managed separately.
## TIMEOUT is in minutes; it sets the go-test deadline AND propagates to
## FORMAE_TEST_TIMEOUT, which the harness uses for its per-command poll
## deadline (defaults to 5min when unset — too short for resources like
## RDS DBInstance / EKS Cluster).
conformance-test-crud-run:
	@echo "Running CRUD conformance tests..."
	@FORMAE_TEST_FILTER="$(TEST)" FORMAE_TEST_TYPE=crud FORMAE_TEST_PARALLEL="$(PARALLEL)" FORMAE_TEST_TIMEOUT="$(or $(TIMEOUT),60)" \
		FORMAE_TEST_ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text 2>/dev/null) \
		$(GO) test -tags=conformance -v -timeout $(or $(TIMEOUT),60)m ./...

## conformance-test-discovery-run: Run only discovery tests (no cleanup)
## Used by CI matrix jobs where cleanup is managed separately.
conformance-test-discovery-run:
	@echo "Running discovery conformance tests..."
	@FORMAE_TEST_FILTER="$(TEST)" FORMAE_TEST_TYPE=discovery FORMAE_TEST_PARALLEL="$(PARALLEL)" FORMAE_TEST_TIMEOUT="$(or $(TIMEOUT),60)" \
		FORMAE_TEST_ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text 2>/dev/null) \
		$(GO) test -tags=conformance -v -timeout $(or $(TIMEOUT),60)m ./...
