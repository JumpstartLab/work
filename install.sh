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
# The repo is private: anonymous release downloads 404, so use an authenticated
# gh when there is one (collaborators run `gh auth login` once), curl otherwise.
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  gh release download -R "$repo" -p "$asset" -p checksums.txt -D "$tmp"
else
  curl -fsSL "$base/$asset" -o "$tmp/$asset" || {
    echo "download failed: $repo is private. Install gh, run 'gh auth login', and re-run." >&2; exit 1; }
  curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
fi
(cd "$tmp" && grep " $asset\$" checksums.txt | { command -v sha256sum >/dev/null && sha256sum -c - || shasum -a 256 -c -; })
mkdir -p "$bin_dir"
install -m 755 "$tmp/$asset" "$bin_dir/work"
echo "installed $bin_dir/work"
case ":$PATH:" in *":$bin_dir:"*) ;; *) echo "add $bin_dir to your PATH" ;; esac
echo "next: work login"
