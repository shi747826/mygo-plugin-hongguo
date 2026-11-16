#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
go_command="${GO:-go}"
export GOWORK=off
export GOOS="${GOOS:-$("$go_command" env GOOS)}"
export GOARCH="${GOARCH:-$("$go_command" env GOARCH)}"
output_dir="${1:-$root/dist}"
mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
package_dir="$(mktemp -d "$output_dir/.package.XXXXXX")"
# Only remove the verified temporary directory created under this output directory.
cleanup() {
  case "$package_dir" in "$output_dir"/.package.*) rm -rf "$package_dir" ;; *) return 1 ;; esac
}
trap cleanup EXIT
cd "$root"
"$go_command" build -trimpath -o "$package_dir/plugin-hongguo" .
chmod 755 "$package_dir/plugin-hongguo"
if [[ "$GOOS" == darwin ]]; then
  if [[ "$(uname -s)" != Darwin ]]; then
    echo "macOS packaging requires codesign on a macOS host" >&2
    exit 1
  fi
  codesign --force --sign - "$package_dir/plugin-hongguo"
  codesign --verify --strict "$package_dir/plugin-hongguo"
fi
cp plugin.json LICENSE THIRD_PARTY_NOTICES.md "$package_dir/"
cp -R licenses "$package_dir/licenses"
archive="$output_dir/hongguo-$GOOS-$GOARCH.zip"
# A fresh temporary archive never retains entries from an older package.
(cd "$package_dir" && zip -q -r package.zip plugin.json plugin-hongguo LICENSE THIRD_PARTY_NOTICES.md licenses)
cp "$package_dir/package.zip" "$archive"
printf 'Installable ZIP: %s\n' "$archive"
