#!/usr/bin/env bash
# Populate npm platform packages from GoReleaser's dist directory before npm
# publish. Usage: scripts/package-npm-artifacts.sh <version> [dist-dir]
set -euo pipefail
version=${1:?version required}
dist_dir=${2:-dist}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 win32-amd64 win32-arm64; do
  os=${target%-*}; arch=${target#*-}
  goarch=$arch; [ "$arch" = amd64 ] && goarch=amd64
	lookup_os=$os
	[ "$os" = win32 ] && lookup_os=windows
	bin_name=jevkit
	[ "$lookup_os" = windows ] && bin_name=jevkit.exe
	source=$(find "$dist_dir" -type f -path "*_${lookup_os}_${goarch}_*/$bin_name" -print -quit)
	[ -n "$source" ] || { echo "missing $lookup_os/$goarch binary in $dist_dir" >&2; exit 1; }
  package_dir="$root/npm/$target"
  mkdir -p "$package_dir/bin"
	cp "$source" "$package_dir/bin/$bin_name"
  chmod 0755 "$package_dir/bin/"*
  sed -i.bak "s/0.0.0-development/$version/g" "$package_dir/package.json"
  rm "$package_dir/package.json.bak"
done
sed -i.bak "s/0.0.0-development/$version/g" "$root/npm/jevkit/package.json"
rm "$root/npm/jevkit/package.json.bak"
