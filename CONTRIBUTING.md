# Contributing

This document covers local development for plugin authors. For user-facing
plugin docs (configuration, supported resources, examples), see
[README.md](README.md).

## Prerequisites

- Go 1.25+
- [Pkl CLI](https://pkl-lang.org/main/current/pkl-cli/index.html) 0.30+
- AWS credentials (for integration/conformance testing)

## Local Installation

```bash
# Install the plugin
make install
```

## Building

```bash
make build      # Build plugin binary
make test-unit  # Run unit tests
make lint       # Run linter
make install    # Build + install locally
```

## Local Testing

```bash
# Install plugin locally
make install

# Start formae agent
formae agent start

# Apply example resources
formae apply --mode reconcile --watch examples/basic/main.pkl
```

## Credentials Setup for Testing

The `scripts/ci/setup-credentials.sh` script verifies AWS credentials before
running conformance tests:

```bash
# Verify credentials are configured
./scripts/ci/setup-credentials.sh

# Run conformance tests (calls setup-credentials automatically)
make conformance-test
```

## Conformance Testing

Run the full CRUD lifecycle + discovery tests:

```bash
make conformance-test  # Latest formae version
```

The `scripts/ci/clean-environment.sh` script cleans up test resources. It runs
before and after conformance tests and is idempotent.
