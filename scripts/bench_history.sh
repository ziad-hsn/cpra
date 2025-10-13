#!/usr/bin/env bash
set -euo pipefail

START_COMMIT="${1:-8e1826db0eff0ccaa70f3c17d10062568039e824}"
PKGS=(./internal/loader/parser ./internal/loader/streaming)
COUNT=3

ROOT_DIR="$(git rev-parse --show-toplevel)"
RESULTS_DIR="$ROOT_DIR/bench-results"
OVERLAY_DIR="$ROOT_DIR/bench/overlay"
mkdir -p "$RESULTS_DIR"

CURRENT_HEAD="$(git rev-parse --verify HEAD)"

COMMITS=( $(git rev-list --reverse ${START_COMMIT}..HEAD) )

echo "Benchmarking ${#COMMITS[@]} commits from $START_COMMIT..HEAD (count=$COUNT)"

for SHA in "${COMMITS[@]}"; do
  WORKTREE="$(mktemp -d)"
  echo "\n===> Checkout $SHA into $WORKTREE"
  git worktree add --detach "$WORKTREE" "$SHA" >/dev/null
  trap 'git worktree remove --force "$WORKTREE" >/dev/null || true' EXIT

  # Overlay benchmark test files without committing
  rsync -a "$OVERLAY_DIR/" "$WORKTREE/" >/dev/null

  pushd "$WORKTREE" >/dev/null
  OUTFILE="$RESULTS_DIR/$SHA.txt"
  echo "Running benches in $SHA..."
  for PKG in "${PKGS[@]}"; do
    echo "# $PKG" >> "$OUTFILE"
    go test -run '^$' -bench '.' -benchmem -count "$COUNT" "$PKG" >> "$OUTFILE" 2>&1 || true
  done
  popd >/dev/null

  git worktree remove --force "$WORKTREE" >/dev/null
  trap - EXIT
done

echo "\nResults saved to $RESULTS_DIR"
echo "To compare latest vs baseline, run:\n  tail -n +1 $RESULTS_DIR/*.txt | sed -n 's/^Benchmark/\0/p'"

