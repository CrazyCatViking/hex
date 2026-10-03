set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Build a development CLI for the current machine.
build:
    go build -trimpath -o bin/hex ./cmd/hex

# Install the CLI into GOBIN or GOPATH/bin.
install:
    go install ./cmd/hex

test:
    go test -race ./...
    go vet ./...
    npm run build
    npm test

format:
    gofmt -w cmd internal server examples
    npm run format

# Update the client version and workspace lockfile without creating a git tag.
version-client version:
    npm version '{{ version }}' --workspace @crazycatviking/hex --no-git-tag-version

check-client:
    npm run build --workspace @crazycatviking/hex
    npm test --workspace @crazycatviking/hex

# Produce a package-manager-installable tarball in dist/npm.
pack-client directory="dist/npm": check-client
    mkdir -p '{{ directory }}'
    npm pack --workspace @crazycatviking/hex --pack-destination '{{ directory }}'

# Verify that an independent app can install and import the packed npm client.
test-client-package:
    node scripts/test-client-package.mjs

# Verify, commit, push, and publish the client; defaults to the next minor version.
publish-client version="":
    node scripts/release.mjs client '{{ version }}'

# Verify release orchestration without publishing to npm or GitHub.
test-releases:
    node --test scripts/release.test.mjs

# Compile all four supported CLI release binaries and their checksums.
build-cli version: (_validate-version version)
    mkdir -p 'dist/cli/{{ version }}'
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags '-s -w -X main.version={{ version }}' -o 'dist/cli/{{ version }}/hex-darwin-arm64' ./cmd/hex
    CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags '-s -w -X main.version={{ version }}' -o 'dist/cli/{{ version }}/hex-darwin-amd64' ./cmd/hex
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w -X main.version={{ version }}' -o 'dist/cli/{{ version }}/hex-windows-amd64.exe' ./cmd/hex
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w -X main.version={{ version }}' -o 'dist/cli/{{ version }}/hex-linux-amd64' ./cmd/hex
    just _cli-checksums '{{ version }}'

# Verify, build, tag, push, and publish the CLI; defaults to the next minor version.
publish-cli version="":
    node scripts/release.mjs cli '{{ version }}'

# Upload already-built binaries for an existing, pushed cli-v<version> tag.
upload-cli version: (_validate-version version)
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ '{{ version }}' == *-* ]]; then
        flags=(--prerelease)
    else
        flags=(--latest)
    fi
    gh release create 'cli-v{{ version }}' dist/cli/{{ version }}/* --repo crazycatviking/hex --verify-tag --title 'Hex CLI {{ version }}' --generate-notes "${flags[@]}"

[private]
_validate-version version:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! [[ '{{ version }}' =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
        printf '%s\n' 'Use a version such as 0.1.0 or 0.2.0-rc.1' >&2
        exit 1
    fi

[private]
_cli-checksums version:
    #!/usr/bin/env bash
    set -euo pipefail
    cd 'dist/cli/{{ version }}'
    shasum -a 256 hex-* > SHA256SUMS
