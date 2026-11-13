#!/bin/sh
# Install `work` into ~/.local/bin (or $WORK_BIN_DIR) from the latest release.
set -eu
repo="JumpstartLab/work"
os="$(uname -s | tr A-Z a-z)"
arch="$(uname -m)"; case "$arch" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
bin_dir="${WORK_BIN_DIR:-$HOME/.local/bin}"
asset="work-$os-$arch"
base="https://github.com/$repo/releases/latest/download"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
(cd "$tmp" && grep " $asset\$" checksums.txt | { command -v sha256sum >/dev/null && sha256sum -c - || shasum -a 256 -c -; })
mkdir -p "$bin_dir"
install -m 755 "$tmp/$asset" "$bin_dir/work"
echo "installed $bin_dir/work"
case ":$PATH:" in *":$bin_dir:"*) ;; *) echo "add $bin_dir to your PATH" ;; esac
echo "next: work login"
