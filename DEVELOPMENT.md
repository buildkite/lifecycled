# Development Guide

## Prerequisites

### Required Tools

- **Go**: Go 1.11 or later (module support enabled)
- **gox**: Cross-platform Go compilation tool

#### Installing gox

```bash
go install github.com/mitchellh/gox@latest
```

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
- linux/amd64
- linux/arm64 & linux/aarch64
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
   git tag v3.x.x
   git push --tags
   ```

2. **Build release binaries:**
   ```bash
   make release
   ```

3. **Generate changelog** (requires `ghch`):
   ```bash
   ghch --format=markdown --from=<old_version> --next-version=v3.x.x
   ```

4. **Create GitHub release:**
   - Navigate to the GitHub repository
   - Create a new release
   - Select the tag created in step 1
   - Use the generated changelog as the release description

5. **Upload binaries:**
   - Manually upload all binaries from the `build/` directory to the GitHub release

6. **Publish the release**

## Code Generation

If you make changes that require code generation:

```bash
make generate
```

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
