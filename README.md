<!-- ============================================================================
     TEMPLATE CHECKLIST - Remove this entire block after completing setup
     ============================================================================

## Getting Started

After creating your plugin with `formae plugin init`, complete these steps:

- [ ] Update `formae-plugin.pkl` with your plugin metadata (name, namespace, description)
- [ ] Define your resource types in `schema/pkl/*.pkl`
- [ ] Implement CRUD operations in `plugin.go`
- [ ] Update this README (see below - replace title, description, resources, etc.)
- [ ] Set up local credentials for testing (see Development section)
- [ ] Run conformance tests locally: `make conformance-test`
- [ ] Configure CI credentials in `.github/workflows/ci.yml` (optional)
- [ ] Remove this "Getting Started" checklist section

For detailed guidance, see the [Plugin SDK Documentation](https://docs.formae.io/plugin-sdk).

     ============================================================================
     END TEMPLATE CHECKLIST - Everything below is YOUR plugin's README
     ============================================================================ -->

# Example Plugin for Formae

<!-- TODO: Update title and description for your plugin -->

Example Formae plugin template - replace this with a description of what your plugin manages.

## Installation

```bash
# Install the plugin
formae plugin install <plugin-name>

# Or build from source
make install
```

## Supported Resources

<!-- TODO: Document your supported resource types -->

| Resource Type | Description |
|---------------|-------------|
| `AWS::Service::Resource` | Example resource (replace with your actual resources) |

## Configuration

Configure a target in your Forma file:

```pkl
new formae.Target {
    label = "my-target"
    namespace = "AWS"  // TODO: Update with your namespace
    config = new Mapping {
        ["region"] = "us-east-1"
        // TODO: Add your provider-specific configuration
    }
}
```

## Examples

See the [examples/](examples/) directory for usage examples.

```bash
# Evaluate an example
formae eval examples/basic/main.pkl

# Apply resources
formae apply --mode reconcile --watch examples/basic/main.pkl
```

## Development

### Prerequisites

- Go 1.25+
- [Pkl CLI](https://pkl-lang.org/main/current/pkl-cli/index.html)
- Cloud provider credentials (for conformance testing)

### Building

```bash
make build      # Build plugin binary
make test       # Run unit tests
make lint       # Run linter
make install    # Build + install locally
```

### Local Testing

```bash
# Install plugin locally
make install

# Start formae agent
formae agent start

# Apply example resources
formae apply --mode reconcile --watch examples/basic/main.pkl
```

### Credentials Setup

The `scripts/ci/setup-credentials.sh` script is used for **local development** to verify your cloud credentials are configured correctly before running conformance tests.

```bash
# Verify credentials are configured
./scripts/ci/setup-credentials.sh

# Run conformance tests (calls setup-credentials automatically)
make conformance-test
```

**For CI/CD**, configure credentials differently using GitHub secrets or OIDC. See `.github/workflows/ci.yml` for examples with AWS, Azure, GCP, and OpenStack.

### Conformance Testing

Run the full CRUD lifecycle + discovery tests:

```bash
make conformance-test                  # Latest formae version
make conformance-test VERSION=0.80.0   # Specific version
```

The `scripts/ci/clean-environment.sh` script cleans up test resources. It runs before and after conformance tests and should be idempotent.

## License

This plugin is licensed under [Apache-2.0](LICENSE). <!-- TODO: Update with your chosen license -->
