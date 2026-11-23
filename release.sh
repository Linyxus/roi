#!/bin/sh
# Tag a release and publish a pre-built binary for macOS on Apple silicon.
# usage: ./release.sh v0.3.0
set -eu

v="${1:-}"
case "$v" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "usage: ./release.sh vMAJOR.MINOR.PATCH" >&2; exit 2 ;;
esac
cd "$(dirname "$0")"

command -v gh >/dev/null || { echo "release: the gh CLI is required" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "release: the working tree has uncommitted changes" >&2; exit 1; }
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || { echo "release: not on main" >&2; exit 1; }
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "release: main is not in sync with origin; push or pull first" >&2; exit 1; }
if git rev-parse -q --verify "refs/tags/$v" >/dev/null || git ls-remote --exit-code --tags origin "$v" >/dev/null 2>&1; then
  echo "release: tag $v already exists" >&2; exit 1
fi

go vet ./...
go test ./...

asset=roi-darwin-arm64.tar.gz
rm -rf dist && mkdir dist
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=${v#v}" -o dist/roi .
tar -C dist -czf "dist/$asset" roi
(cd dist && shasum -a 256 "$asset" > "$asset.sha256")
rm dist/roi

git tag -a "$v" -m "roi $v"
git push origin "$v"
gh release create "$v" "dist/$asset" "dist/$asset.sha256" --title "roi $v" --generate-notes --verify-tag
echo "Released $v: $(gh release view "$v" --json url -q .url)"
