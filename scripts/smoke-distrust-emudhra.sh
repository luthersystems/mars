#!/usr/bin/env bash
# Verify the mars image trusts no eMudhra (emSign) root CA (CVE-2026-111943).
#
# Why: every customer Terraform deploy, Ansible run and cloud CLI call runs in
# this image, and the keys of eight eMudhra intermediates leaked. The
# Dockerfile runs buildenv's distrust-emudhra.sh last; this check proves the
# result on the built image, on each architecture, so a new tool that brings
# its own CA bundle (a pip package, a gcloud or AWS CLI upgrade) cannot
# re-add a root unnoticed.
#
# How: the image only lists and archives the files. Each file is read on the
# host, with the host's openssl, so a broken tool in the image cannot make
# the check pass. Two independent tests run on every file in the image that
# holds a PEM certificate (plain, X509 or OpenSSL TRUSTED), not only on the
# bundles the script knows about:
#   - each certificate's subject must not contain "emsign" (any case);
#   - the file must not contain a line of an emSign root's PEM text (the same
#     lines distrust-emudhra.sh matches), which also catches a block openssl
#     cannot parse.
# The system bundle and OpenSSL's hash directory (/etc/ssl/certs) must also
# hold at least 100 roots, so a trust store that was emptied fails instead of
# passing as clean. A Java keystore (cacerts) fails the check: the image has
# no Java today, and a new one would need keytool to read it.
#
# Roots compiled into a program are not text, so the scan above skips them.
# Every binary file (one with a NUL byte) is also searched for an emSign
# root's PEM line or for the common name an emSign root has in DER
# ("emSign Root CA", "emSign ECC Root CA"); a match fails the check. This
# covers a Go program built with golang.org/x/crypto/x509roots/fallback and
# Node.js, whose own root list holds all four emSign roots. Node is the one
# exception: its root list is always in the binary, so each node executable
# is run in the image instead (with the image's environment, so NODE_OPTIONS
# applies) and fails if the CA certificates it uses hold an emSign root. The
# image has no Node today; a new one needs NODE_OPTIONS=--use-openssl-ca.
#
# Usage: scripts/smoke-distrust-emudhra.sh IMAGE
set -euo pipefail

IMAGE="${1:?usage: smoke-distrust-emudhra.sh <image>}"

# Line 4 of each emSign root's PEM text. Read from the vendored
# distrust-emudhra.sh, so the two cannot drift apart when buildenv adds a root.
# The script stores each line split by "_" so it never matches itself.
mapfile -t EMSIGN_LINES < <(
	sed -n '/^EMSIGN_SPLIT="$/,/^"$/p' "$(dirname "$0")/distrust-emudhra.sh" | grep -v '"' | tr -d _
)
if [ "${#EMSIGN_LINES[@]}" -lt 4 ]; then
	echo "FAIL: could not read EMSIGN_LINES from distrust-emudhra.sh" >&2
	exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/root"

# List every file that holds a PEM certificate, every Java keystore, and the
# hash directory, then stream them out as one tar (-h follows the links in
# /etc/ssl/certs into /usr/share/ca-certificates). -xdev keeps find on the
# image's own file system (not /proc or /sys). Binary files that hold an
# emSign root (see above) go in the same tar, listed in BINLIST, and the node
# executables are listed in NODELIST.
BINLIST=tmp/smoke-emsign-binaries
NODELIST=tmp/smoke-emsign-nodes
grep_args=(-e "emSign Root CA" -e "emSign ECC Root CA")
for line in "${EMSIGN_LINES[@]}"; do
	grep_args+=(-e "$line")
done
docker run --rm --user 0 --entrypoint /bin/bash "$IMAGE" -c '
set -euo pipefail
find / -xdev -type f -print0 2>/dev/null >/tmp/allfiles
{
	xargs -0 grep -lIE -e "-----BEGIN (TRUSTED |X509 )?CERTIFICATE-----" </tmp/allfiles 2>/dev/null || true
	find / -xdev -type f -name cacerts 2>/dev/null || true
	find /etc/ssl/certs -maxdepth 1 \( -name "*.pem" -o -name "*.[0-9]" \) 2>/dev/null || true
} | sort -u >/tmp/certfiles
# grep -a reads binaries as text; keep only the files with a NUL byte (the
# text files are checked above, and a stripped bundle keeps its comments).
xargs -0 grep -laF "$@" </tmp/allfiles 2>/dev/null | while IFS= read -r f; do
	if [ "$(tr -d "\\000" <"$f" | wc -c)" -ne "$(wc -c <"$f")" ]; then
		printf "%s\n" "$f"
	fi
done | sort -u >/'"$BINLIST"'
find / -xdev -type f \( -name node -o -name nodejs \) -perm -u+x 2>/dev/null | sort -u >/'"$NODELIST"'
cat /tmp/certfiles /'"$BINLIST"' | sort -u >/tmp/tarfiles
printf "%s\n" /'"$BINLIST"' /'"$NODELIST"' >>/tmp/tarfiles
tar -chf - -T /tmp/tarfiles 2>/dev/null
' bash "${grep_args[@]}" | tar -xf - -C "$work/root"
if [ ! -f "$work/root/$BINLIST" ] || [ ! -f "$work/root/$NODELIST" ]; then
	echo "FAIL: could not list the binary files and node executables in $IMAGE" >&2
	exit 1
fi

fail=0

# check_bundle NAME FILE MIN: set count to the number of certificates in
# FILE, and fail if a certificate in FILE has an emSign
# subject, or if FILE has fewer than MIN certificates. A block openssl cannot
# read fails only when MIN > 0 (a trust store the image's TLS clients load);
# elsewhere (test fixtures in Python packages) it is skipped, and the
# PEM-line test below still covers it.
check_bundle() {
	local name=$1 file=$2 min=$3 dir n=0 bad=0 subject pem
	dir=$(mktemp -d -p "$work")
	awk -v dir="$dir" '
		/^-----BEGIN (TRUSTED |X509 )?CERTIFICATE-----/ { n++; out = sprintf("%s/%05d.pem", dir, n); inblk = 1 }
		inblk { print > out }
		/^-----END (TRUSTED |X509 )?CERTIFICATE-----/ { inblk = 0; close(out) }
	' "$file"
	for pem in "$dir"/*.pem; do
		[ -e "$pem" ] || continue
		n=$((n + 1))
		if ! subject=$(openssl x509 -noout -subject -in "$pem" 2>&1); then
			if [ "$min" -gt 0 ]; then
				echo "FAIL: $name: openssl cannot read certificate $n: $subject" >&2
				bad=$((bad + 1))
			fi
			continue
		fi
		if grep -qi 'emsign' <<<"$subject"; then
			echo "FAIL: $name: $subject" >&2
			bad=$((bad + 1))
		fi
	done
	rm -rf "$dir"
	if [ "$bad" -gt 0 ]; then
		fail=1
	fi
	if [ "$n" -lt "$min" ]; then
		echo "FAIL: $name: $n certificate(s), expected at least $min" >&2
		fail=1
	fi
	count=$n
}

bundle="$work/root/etc/ssl/certs/ca-certificates.crt"
if [ ! -f "$bundle" ]; then
	echo "FAIL: /etc/ssl/certs/ca-certificates.crt is missing" >&2
	exit 1
fi
check_bundle /etc/ssl/certs/ca-certificates.crt "$bundle" 100
echo "/etc/ssl/certs/ca-certificates.crt: $count certificate(s)"

# OpenSSL's hash directory, which curl, git and Python's ssl module read.
: >"$work/certdir.crt"
for f in "$work"/root/etc/ssl/certs/*.pem "$work"/root/etc/ssl/certs/*.[0-9]; do
	[ -f "$f" ] && cat "$f" >>"$work/certdir.crt"
done
check_bundle "/etc/ssl/certs/*.pem and hash links" "$work/certdir.crt" 100
# Each root is counted twice here: once as its *.pem link and once as its
# hash link.
echo "/etc/ssl/certs/*.pem and hash links: $count certificate(s)"

# Every other file with a PEM certificate: certifi, botocore, the AWS CLI,
# google-cloud-cli, httplib2 and any bundle a later tool adds.
files=0
while IFS= read -r -d '' f; do
	path=${f#"$work/root"}
	if [ "$path" = "/$BINLIST" ] || [ "$path" = "/$NODELIST" ] || grep -qxF "$path" "$work/root/$BINLIST"; then
		continue
	fi
	case $path in
	*/cacerts)
		echo "FAIL: $path: a Java keystore; this check cannot read it" >&2
		fail=1
		continue
		;;
	esac
	files=$((files + 1))
	check_bundle "$path" "$f" 0
	for line in "${EMSIGN_LINES[@]}"; do
		if grep -qF "$line" "$f"; then
			echo "FAIL: $path: holds an emSign root's PEM text" >&2
			fail=1
			break
		fi
	done
done < <(find "$work/root" -type f -print0)
echo "scanned $files file(s) with a PEM certificate"

# Binary files that hold an emSign root. Each is read again here, with the
# host's grep, so the image's grep cannot make one pass; a node executable
# is left to the check below.
bins=0
while IFS= read -r path; do
	[ -n "$path" ] || continue
	if grep -qxF "$path" "$work/root/$NODELIST"; then
		continue
	fi
	bins=$((bins + 1))
	if grep -qaF "${grep_args[@]}" "$work/root$path"; then
		echo "FAIL: $path: a binary file that holds an emSign root" >&2
		fail=1
	else
		echo "FAIL: $path: the image found an emSign root in it, the host did not" >&2
		fail=1
	fi
done <"$work/root/$BINLIST"
echo "found $bins binary file(s) with an emSign root, not counting node"

# Node.js. tls.getCACertificates("default") (Node 22.15 and later) lists the
# certificates Node trusts with its options. An older Node has only
# tls.rootCertificates, its own list, which holds the emSign roots, so it
# passes only with --use-openssl-ca, which makes it read the system bundle
# checked above. A node that cannot run fails.
NODE_JS='
const tls = require("tls");
const { X509Certificate } = require("crypto");
// Whole arguments only: execArgv also holds the text of this script.
const opts = (process.env.NODE_OPTIONS || "").split(/\s+/).concat(process.execArgv);
let certs, how;
if (typeof tls.getCACertificates === "function") {
  certs = tls.getCACertificates("default");
  how = "tls.getCACertificates(default)";
} else if (opts.includes("--use-openssl-ca") && !opts.includes("--use-bundled-ca")) {
  certs = [];
  how = "--use-openssl-ca (the system bundle)";
} else {
  certs = tls.rootCertificates;
  how = "its own root list";
}
const bad = certs.map((p) => new X509Certificate(p).subject).filter((s) => /emsign/i.test(s));
for (const s of bad) console.log("emSign: " + s.replace(/\n/g, ", "));
console.log(process.version + " uses " + how + ": " + certs.length + " certificate(s), " + bad.length + " emSign");
process.exit(bad.length ? 1 : 0);
'
nodes=0
while IFS= read -r node; do
	[ -n "$node" ] || continue
	nodes=$((nodes + 1))
	if out=$(docker run --rm --entrypoint "$node" "$IMAGE" -e "$NODE_JS" 2>&1); then
		echo "$node: $out"
	else
		echo "FAIL: $node trusts an emSign root or cannot run: $out" >&2
		fail=1
	fi
done <"$work/root/$NODELIST"
echo "checked $nodes node executable(s)"

if [ "$fail" -ne 0 ]; then
	echo "FAIL: $IMAGE trusts an emSign root" >&2
	exit 1
fi
echo "OK:   $IMAGE has no emSign root"
