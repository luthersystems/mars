#!/bin/sh
# block-archiver.sh CHAINS_DIR ARCHIVE_DIR [INTERVAL_SECONDS]
#
# Moves the peer's sealed block files to a second, cheaper volume and leaves
# a symlink in their place, so the hot path (the current block file, the
# block index and the state, history and private data stores) stays on the
# fast volume, and the bulk of the ledger sits on the cheap one.
#
# Fabric (common/ledger/blkstorage) writes blocks to
# CHAINS_DIR/<channel>/blockfile_NNNNNN, appends to the highest-numbered file
# only, and starts the next file when that one is full (64 MiB). It never
# writes to a lower-numbered (sealed) file again, and it opens a file by its
# path for each read, so a symlink is read through. On start it may truncate
# the highest-numbered file only. So every file below the highest number is
# safe to move.
#
# For each sealed file that is still a regular file:
#   1. copy it to ARCHIVE_DIR/<channel>/.NAME.tmp, fsync it
#   2. rename the copy to NAME, fsync the directory
#   3. compare the copy with the original; stop if they differ
#   4. make a symlink .NAME.link to the copy and rename it over the original
#      (rename(2) is atomic: a reader opens the old file or the symlink)
#   5. fsync the channel directory
# Fabric lists only names that start with "blockfile_", so the hidden
# temporary names are not seen. A crash at any step leaves either the
# original file or a complete copy behind a symlink; the next pass finishes
# or repeats the work.
#
# INTERVAL_SECONDS 0 runs one pass and exits (for tests).

set -u

chains=${1:?usage: block-archiver.sh CHAINS_DIR ARCHIVE_DIR [INTERVAL_SECONDS]}
archive=${2:?usage: block-archiver.sh CHAINS_DIR ARCHIVE_DIR [INTERVAL_SECONDS]}
interval=${3:-60}

log() { echo "block-archiver: $*"; }

# fsync a file or directory. coreutils sync takes a path (8.24 and later);
# busybox sync takes it with -f only for a filesystem, so fall back to a full
# sync there.
fsync() { sync "$1" 2>/dev/null || sync; }

# archive_channel DIR: archive every sealed block file of one channel.
archive_channel() {
  dir=$1
  ch=$(basename "$dir")
  # Fabric pads the number to six digits, so the glob sorts them in order.
  last=""
  for p in "$dir"/blockfile_[0-9]*; do
    [ -e "$p" ] || [ -L "$p" ] || continue
    last=${p##*/}
  done
  [ -n "$last" ] || return 0
  for src in "$dir"/blockfile_[0-9]*; do
    f=${src##*/}
    [ "$f" = "$last" ] && continue
    [ -L "$src" ] && continue
    [ -f "$src" ] || continue
    dst="$archive/$ch/$f"
    mkdir -p "$archive/$ch" || return 1
    if ! cmp -s "$src" "$dst" 2>/dev/null; then
      if ! { cp "$src" "$archive/$ch/.$f.tmp" &&
        fsync "$archive/$ch/.$f.tmp" &&
        mv -f "$archive/$ch/.$f.tmp" "$dst" &&
        fsync "$archive/$ch"; }; then
        log "copy of $src failed"
        rm -f "$archive/$ch/.$f.tmp"
        return 1
      fi
      if ! cmp -s "$src" "$dst"; then
        log "copy of $src differs, keeping the original"
        return 1
      fi
    fi
    if ! { ln -sfn "$dst" "$dir/.$f.link" &&
      mv -f "$dir/.$f.link" "$src" &&
      fsync "$dir"; }; then
      log "symlink of $src failed"
      rm -f "$dir/.$f.link"
      return 1
    fi
    log "archived $ch/$f"
  done
}

pass() {
  status=0
  [ -d "$chains" ] || return 0
  for d in "$chains"/*/; do
    [ -d "$d" ] || continue
    archive_channel "${d%/}" || status=1
  done
  return $status
}

while :; do
  pass
  rc=$?
  [ "$interval" -gt 0 ] || exit "$rc"
  sleep "$interval"
done
