#!/usr/bin/env bash
# Checks the release build: the cross builds, the version string, and the
# README quick start. "make test-release" runs this script.
set -euo pipefail
cd "$(dirname "$0")/.."

make cross VERSION=test
for f in bin/tether-linux-amd64 bin/tether-linux-arm64 bin/tether-darwin-arm64; do
  test -f "$f" || { echo "check_release: missing $f"; exit 1; }
done

# Only a linux/amd64 host can run the linux-amd64 binary.
if [ "$(uname -s)" = "Linux" ] && [ "$(uname -m)" = "x86_64" ]; then
  ./bin/tether-linux-amd64 version | grep -q "tether test" ||
    { echo "check_release: the version output does not contain 'tether test'"; exit 1; }
else
  echo "check_release: skip the version check; this host is not linux/amd64"
fi

grep -q "tether setup" README.md ||
  { echo "check_release: README.md does not contain 'tether setup'"; exit 1; }

if command -v goreleaser >/dev/null 2>&1; then
  goreleaser check
else
  echo "check_release: skip 'goreleaser check'; goreleaser is not installed"
fi

echo "check_release: PASS"
