#!/usr/bin/env bash
# Tests ansible-roles/k8s_fabric_peer/files/fabric-peer/files/block-archiver.sh
# against a fake peer chains directory.
#
#   scripts/test-block-archiver.sh                 # with this host's sh
#   BUSYBOX_IMAGE=busybox:1.35.0 scripts/test-block-archiver.sh
#                                                  # in the sidecar's image
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ARCHIVER="${ROOT}/ansible-roles/k8s_fabric_peer/files/fabric-peer/files/block-archiver.sh"
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

# archiver ARGS...: one pass of the archiver, with the host's sh or in the
# busybox image the chart runs it in (the same paths mounted in).
archiver() {
  if [ -n "${BUSYBOX_IMAGE:-}" ]; then
    # As this user, so the test can still write and remove what it makes.
    docker run --rm --user "$(id -u):$(id -g)" \
      -v "$T:$T" -v "$ARCHIVER:/block-archiver.sh:ro" \
      "$BUSYBOX_IMAGE" sh /block-archiver.sh "$@"
  else
    sh "$ARCHIVER" "$@"
  fi
}

# fails CMD...: CMD exits non-zero (its output dropped).
fails() { ! "$@" >/dev/null 2>&1; }

fail=0
check() {
  local desc=$1
  shift
  if "$@"; then echo "OK:   $desc"; else
    echo "FAIL: $desc"
    fail=1
  fi
}

chains="$T/prod/chains"
archive="$T/archive"
mkdir -p "$chains/luther" "$chains/other"
for n in 000000 000001 000002; do head -c 1000 /dev/urandom >"$chains/luther/blockfile_$n"; done
head -c 1000 /dev/urandom >"$chains/other/blockfile_000000"
cp "$chains/luther/blockfile_000000" "$T/orig0"
cp "$chains/luther/blockfile_000002" "$T/orig2"

archiver "$chains" "$archive" 0 >"$T/out1"

check "sealed file 0 is a symlink" test -L "$chains/luther/blockfile_000000"
check "sealed file 1 is a symlink" test -L "$chains/luther/blockfile_000001"
check "current file stays a regular file" sh -c "[ -f '$chains/luther/blockfile_000002' ] && [ ! -L '$chains/luther/blockfile_000002' ]"
check "current file is unchanged" cmp -s "$T/orig2" "$chains/luther/blockfile_000002"
check "a channel with one file is untouched" sh -c "[ ! -L '$chains/other/blockfile_000000' ] && [ ! -e '$archive/other' ]"
check "symlink points into the archive" test "$(readlink "$chains/luther/blockfile_000000")" = "$archive/luther/blockfile_000000"
check "read through the symlink gives the original bytes" cmp -s "$T/orig0" "$chains/luther/blockfile_000000"
check "no temporary names left" sh -c "! ls -A '$chains/luther' '$archive/luther' | grep -E '\.(tmp|link)$'"
check "logs each archived file" test "$(grep -c '^block-archiver: archived luther/' "$T/out1")" -eq 2

# A second pass does nothing.
archiver "$chains" "$archive" 0 >"$T/out2"
check "second pass archives nothing" test ! -s "$T/out2"

# Fabric starts a new file: the previous current file is now sealed.
head -c 1000 /dev/urandom >"$chains/luther/blockfile_000003"
archiver "$chains" "$archive" 0 >"$T/out3"
check "the newly sealed file is archived" test -L "$chains/luther/blockfile_000002"
check "the new current file is not" sh -c "[ ! -L '$chains/luther/blockfile_000003' ]"
check "archived bytes of file 2 match" cmp -s "$T/orig2" "$archive/luther/blockfile_000002"

# A crash after the copy but before the symlink: the copy exists, the
# original is still a regular file. The next pass finishes it.
head -c 1000 /dev/urandom >"$chains/luther/blockfile_000004"
cp "$chains/luther/blockfile_000003" "$archive/luther/blockfile_000003"
archiver "$chains" "$archive" 0 >/dev/null
check "a pass after a crash links the existing copy" test -L "$chains/luther/blockfile_000003"

# A stale or partial copy in the archive is replaced, not trusted.
head -c 1000 /dev/urandom >"$chains/luther/blockfile_000005"
echo partial >"$archive/luther/blockfile_000004"
archiver "$chains" "$archive" 0 >/dev/null
check "a partial copy is replaced" sh -c "[ -L '$chains/luther/blockfile_000004' ] && [ \$(wc -c <'$archive/luther/blockfile_000004') -eq 1000 ]"

# An archive that cannot be written keeps the originals and fails the pass.
chmod 0555 "$archive/luther"
head -c 1000 /dev/urandom >"$chains/luther/blockfile_000006"
# (Root writes anyway, so only as another user.)
if [ "$(id -u)" -ne 0 ]; then
  check "an unwritable archive fails the pass" fails archiver "$chains" "$archive" 0
  check "and keeps the original" sh -c "[ ! -L '$chains/luther/blockfile_000005' ] && [ -f '$chains/luther/blockfile_000005' ]"
fi
chmod 0755 "$archive/luther"

# No chains directory yet (a new peer): nothing to do, no error.
check "a missing chains directory is fine" archiver "$T/none" "$archive" 0

exit "$fail"
