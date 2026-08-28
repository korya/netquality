#!/usr/bin/env sh
# Print the CHANGELOG.md section for version $1 (without the leading "v").
# Exits 1 if there is no such section, so a release never ships empty notes.
set -eu
version="${1#v}"
notes=$(awk -v v="$version" '
  /^## \[/ { in_section = ($0 ~ "^## \\[" v "\\]") ; next }
  in_section && /^\[.*\]: / { next }
  in_section { print }
' CHANGELOG.md | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')
if [ -z "$notes" ]; then
  echo "changelog-notes: no '## [$version]' section in CHANGELOG.md" >&2
  exit 1
fi
printf '%s\n' "$notes"
