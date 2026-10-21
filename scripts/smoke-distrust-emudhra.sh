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
# Usage: scripts/smoke-distrust-emudhra.sh IMAGE
set -euo pipefail

IMAGE="${1:?usage: smoke-distrust-emudhra.sh <image>}"

# Line 4 of each emSign root's PEM text. Read from the vendored
# distrust-emudhra.sh, so the two cannot drift apart when buildenv adds a root.
mapfile -t EMSIGN_LINES < <(
	sed -n '/^EMSIGN_LINES="$/,/^"$/p' "$(dirname "$0")/distrust-emudhra.sh" | grep -v '"'
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
# image's own file system (not /proc or /sys).
docker run --rm --user 0 --entrypoint /bin/bash "$IMAGE" -c '
set -euo pipefail
{
	find / -xdev -type f -print0 2>/dev/null |
		xargs -0 grep -lIE -e "-----BEGIN (TRUSTED |X509 )?CERTIFICATE-----" 2>/dev/null || true
	find / -xdev -type f -name cacerts 2>/dev/null || true
	find /etc/ssl/certs -maxdepth 1 \( -name "*.pem" -o -name "*.[0-9]" \) 2>/dev/null || true
} | sort -u >/tmp/certfiles
tar -chf - -T /tmp/certfiles 2>/dev/null
' | tar -xf - -C "$work/root"

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
	case $path in
	/usr/local/sbin/distrust-emudhra.sh)
		# The removal script holds the PEM lines it looks for.
		continue
		;;
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

if [ "$fail" -ne 0 ]; then
	echo "FAIL: $IMAGE trusts an emSign root" >&2
	exit 1
fi
echo "OK:   $IMAGE has no emSign root"
