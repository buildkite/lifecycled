# Development Guide

## Prerequisites

### Required Tools

- **Go**: Go 1.23.0 or later (required for module support and modern syntax)
- **gox**: Cross-platform Go compilation tool (required for `make release`)
- **mockgen**: Mock code generation tool (required for `make generate`)

#### Installing gox

```bash
go install github.com/mitchellh/gox@latest
```

#### Installing mockgen

```bash
go install go.uber.org/mock/mockgen@latest
```

**Note**: This project uses `go.uber.org/mock` (the actively maintained fork) rather than the deprecated `github.com/golang/mock`.

#### PATH Configuration

Ensure your Go bin directory is in your PATH:
```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

## Building

### Local Development Build

Build a binary for your current platform:

```bash
make lifecycled
```

This creates a `lifecycled` binary in the current directory.

### Testing

Run tests, formatting, and vet checks:

```bash
make test
```

This will:
- Format code with `gofmt`
- Run `go vet` for static analysis
- Run all tests with race detection

### Clean Build Artifacts

```bash
make clean
```

## Release Process

### Building Release Binaries

The current release process uses `gox` for cross-platform compilation:

```bash
make release
```

This builds binaries for the following platforms:
- freebsd/amd64
- linux/386
- linux/aarch64
- linux/amd64
- linux/arm64
- windows/amd64

**Output Location:** All binaries are placed in the `build/` directory with the naming pattern:
```
build/lifecycled-{OS}-{Arch}
```

### Version Injection

The build process automatically injects version information from git tags:

```bash
VERSION=$(git describe --tags --candidates=1 --dirty 2>/dev/null || echo "dev")
```

This version is embedded into the binary using Go build flags:
```bash
-ldflags="-s -w -X main.Version=$(VERSION)"
```

### Manual Release Steps

1. **Create and push a git tag:**
   ```bash
   git tag v1.2.3
   git push --tags
   ```

2. **Build release binaries:**
   ```bash
   make release
   ```

3. **Create GitHub release:**
   - Navigate to the GitHub repository
   - Create a new release
   - Select the tag created in step 1
   - Set the Release title to the tag (`v1.2.3`)
   - Set the Previous tag to the tag of the most previous release
   - Click Generate Release Notes

4. **Upload binaries:**
   - Manually upload all binaries from the `build/` directory to the GitHub release

5. **Publish the release**

## Code Generation

The project uses code generation to create mock implementations for testing. If you make changes to interfaces or need to regenerate mocks:

```bash
make generate
```

This will regenerate mock files in the `mocks/` directory using `mockgen` from `go.uber.org/mock`.

**Generated files:**
- `mocks/mock_autoscaling_client.go`
- `mocks/mock_sns_client.go`
- `mocks/mock_sqs_client.go`

## Project Structure

```
.
├── cmd/
│   └── lifecycled/        # Main application entry point
├── build/                 # Build output directory (created by make release)
├── .buildkite/           # Buildkite CI configuration (historical)
└── release/              # Legacy Docker-based build configuration
```

## Development Notes

- The project uses Go modules (`GO111MODULE=on`)
- Version information is derived from git tags
- The `.buildkite/` directory contains historical CI configuration
- There is an experimental `triarius/goreleaser` branch with GoReleaser configuration (not merged)
- Mock generation uses `go.uber.org/mock` (the actively maintained fork of gomock), not the deprecated `github.com/golang/mock`
- Generated mock files should not be manually edited; regenerate them using `make generate`
