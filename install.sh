#!/bin/sh
# Install the latest roi release.
#   curl -fsSL https://raw.githubusercontent.com/Linyxus/roi/main/install.sh | sh
# ROI_INSTALL_DIR chooses the folder; ROI_VERSION (e.g. v0.2.0) pins a release.
set -eu

repo=Linyxus/roi
asset=roi-darwin-arm64.tar.gz

if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then
  echo "roi: pre-built binaries exist only for macOS on Apple silicon." >&2
  echo "Build from source instead: https://github.com/$repo#install" >&2
  exit 1
fi

on_path() { case ":$PATH:" in *":$1:"*) return 0 ;; esac; return 1; }

dir="${ROI_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if on_path "$HOME/.local/bin"; then dir="$HOME/.local/bin"
  elif on_path /usr/local/bin && [ -w /usr/local/bin ]; then dir=/usr/local/bin
  else dir="$HOME/.local/bin"
  fi
fi

if [ -n "${ROI_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/$ROI_VERSION"
else
  base="https://github.com/$repo/releases/latest/download"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "Downloading roi…"
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/$asset.sha256" -o "$tmp/$asset.sha256"
(cd "$tmp" && shasum -a 256 -c "$asset.sha256" >/dev/null) || { echo "roi: the download does not match its checksum" >&2; exit 1; }
tar -C "$tmp" -xzf "$tmp/$asset"

mkdir -p "$dir"
# Replace by rename, so a running server keeps its old file until restarted.
mv -f "$tmp/roi" "$dir/roi.new"
chmod 755 "$dir/roi.new"
mv -f "$dir/roi.new" "$dir/roi"

echo "Installed roi $("$dir/roi" version | cut -d' ' -f2) to $dir/roi"
if ! on_path "$dir"; then
  echo
  echo "$dir is not on your PATH. Add it, for example:"
  echo "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc"
fi
echo
echo "Next:"
echo "  roi login      sign the device in to your tailnet"
echo "  roi install    start at login and keep running"
echo "  roi pair       QR code to open on your phone"
