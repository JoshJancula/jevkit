#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-with-docker.sh [--install]

Build a native jevkit binary using the pinned Docker Compose toolchain.
--install copies it to ~/.local/bin/jevkit after a successful build.
EOF
}

install_binary=false
case "${1:-}" in
  "") ;;
  --install) install_binary=true ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

case "$(uname -s)" in
  Darwin) target_os=darwin ;;
  Linux) target_os=linux ;;
  *) echo "unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  arm64|aarch64) target_arch=arm64 ;;
  x86_64|amd64) target_arch=amd64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

docker compose run --rm \
  -e CGO_ENABLED=0 \
  -e GOOS="$target_os" \
  -e GOARCH="$target_arch" \
  dev make build

if [[ "$target_os" == darwin ]]; then
  ./bin/jevkit version
fi

if [[ "$install_binary" == true ]]; then
  install_dir="${JEVKIT_INSTALL_DIR:-$HOME/.local/bin}"
  mkdir -p "$install_dir"
  install -m 0755 ./bin/jevkit "$install_dir/jevkit"
  echo "installed $install_dir/jevkit"
  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *) echo "add $install_dir to PATH to run: jevkit" >&2 ;;
  esac
fi
