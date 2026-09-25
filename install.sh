#!/bin/sh
# Install jevkit from GitHub Releases. Source with JEVKIT_INSTALL_SOURCE_ONLY=1
# to exercise helper functions without installing anything.
set -eu

REPO="JoshJancula/jevkit"

fail() { printf '%s\n' "jevkit install: $*" >&2; exit 1; }

detect_os() {
  case "$(uname -s)" in
    Darwin) printf '%s\n' darwin ;;
    Linux) printf '%s\n' linux ;;
    *) fail "unsupported operating system: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf '%s\n' amd64 ;;
    arm64|aarch64) printf '%s\n' arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else fail "need sha256sum or shasum to verify the release"; fi
}

verify_checksum() {
  archive=$1 checksums=$2
  expected=$(awk -v name="$(basename "$archive")" '$2 == name || $2 == "*"name {print $1; exit}' "$checksums")
  [ -n "$expected" ] || fail "checksum missing for $(basename "$archive")"
  actual=$(sha256_file "$archive")
  [ "$actual" = "$expected" ] || fail "checksum verification failed"
}

release_version() {
  if [ -n "${JEVKIT_VERSION:-}" ]; then printf '%s\n' "${JEVKIT_VERSION#v}"; return; fi
  curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1
}

main() {
  command -v curl >/dev/null 2>&1 || fail "curl is required"
  os=$(detect_os) arch=$(detect_arch)
  version=$(release_version)
  [ -n "$version" ] || fail "could not determine latest release; set JEVKIT_VERSION"
  archive="jevkit_${version}_${os}_${arch}.tar.gz"
  base="https://github.com/$REPO/releases/download/v$version"
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/jevkit.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT HUP INT TERM
  curl -fL "$base/$archive" -o "$tmp/$archive"
  curl -fL "$base/checksums.txt" -o "$tmp/checksums.txt"
  verify_checksum "$tmp/$archive" "$tmp/checksums.txt"
  if command -v cosign >/dev/null 2>&1; then
    curl -fsSL "$base/checksums.txt.sig" -o "$tmp/checksums.txt.sig" || fail "cosign signature download failed"
    cosign verify-blob --signature "$tmp/checksums.txt.sig" "$tmp/checksums.txt" >/dev/null || fail "cosign verification failed"
  fi
  tar -xzf "$tmp/$archive" -C "$tmp"
  bin=$(find "$tmp" -type f -name jevkit -perm -u+x | head -n 1)
  [ -n "$bin" ] || fail "release archive did not contain executable"
  dest="${JEVKIT_INSTALL_DIR:-$HOME/.local/bin}"
  mkdir -p "$dest"
  install -m 0755 "$bin" "$dest/jevkit"
  printf '%s\n' "installed $dest/jevkit"
  case ":$PATH:" in *":$dest:"*) ;; *) printf '%s\n' "warning: $dest is not on PATH" >&2;; esac
  printf '%s\n' "next: jevkit key set && jevkit install <agent>"
}

[ "${JEVKIT_INSTALL_SOURCE_ONLY:-}" = 1 ] || main "$@"
