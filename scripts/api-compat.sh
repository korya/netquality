#!/usr/bin/env sh
# Report incompatible changes to the public API since the newest release tag.
#
# Compares every exported package against its last released form with
# golang.org/x/exp/cmd/apidiff. Prints a report and exits 1 when anything
# incompatible is found. apidiff itself always exits 0, so the empty report is
# the pass condition, not the exit status.
#
# Usage: scripts/api-compat.sh [tag]   (default: the highest version tag)
#
# Public packages only. Aliasing an internal type into a public one puts it
# beyond apidiff's reach, which is why the library declares its own (INV-7's
# neighbour: see docs/architecture.md on the engine seam).
set -eu

pkgs=". ./server"
tag="${1:-$(git tag --sort=-v:refname | head -1)}"
if [ -z "$tag" ]; then
  echo "api-compat: no version tag to compare against" >&2
  exit 1
fi

# Pinned like every other CI tool, and never added to go.mod (INV-5).
# Override with an installed binary to iterate faster: APIDIFF=apidiff ...
APIDIFF="${APIDIFF:-go run golang.org/x/exp/cmd/apidiff@v0.0.0-20260908205506-85c1c2202aba}"

work=$(mktemp -d)
old="$work/old"
trap 'git worktree remove --force "$old" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
git worktree add --detach "$old" "$tag" >/dev/null 2>&1

echo "comparing the public API against $tag"
status=0
for pkg in $pkgs; do
  slug=$(printf '%s' "$pkg" | tr -c 'A-Za-z0-9' '_')
  (cd "$old" && $APIDIFF -w "$work/old$slug" "$pkg")
  $APIDIFF -w "$work/new$slug" "$pkg"
  $APIDIFF -incompatible "$work/old$slug" "$work/new$slug" > "$work/report$slug"
  if [ -s "$work/report$slug" ]; then
    echo "--- $pkg: incompatible with $tag"
    cat "$work/report$slug"
    status=1
  else
    echo "--- $pkg: compatible with $tag"
  fi
done
exit $status
