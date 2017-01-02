#!/bin/bash
set -eu -o pipefail

if [[ ! "$BUILDKITE_TAG" =~ ^v ]] ; then
  echo "Skipping non-tag build"
  exit 0
fi

VERSION="$(git describe --tags --candidates=1 2>/dev/null || echo dev)"

download_github_release() {
  wget -N https://github.com/c4milo/github-release/releases/download/v1.0.8/github-release_v1.0.8_linux_amd64.tar.gz
  tar -vxf github-release_v1.0.8_linux_amd64.tar.gz
}

github_release() {
  local version="$1"
  ./github-release lox/lifecyled "$version" "$BUILDKITE_COMMIT" "$(git cat-file -p "$version" | tail -n +6)" 'build/*'
}

download_github_release

echo "--- Downloading build artifacts"
buildkite-agent artifact download 'build/*' .

echo "+++ Releasing version ${VERSION} on github.com"
github_release "${VERSION}"