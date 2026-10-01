#!/bin/sh
# Stages into $1 exactly what the transcoder loads, for a FROM-scratch image.
#
# lddtree follows each file's DT_NEEDED tree: the worker (a purego binary,
# so it brings the dynamic linker and libc), the ffgo shim, every FFmpeg
# library and, until Phase 5, the ffmpeg and ffprobe executables. What no
# DT_NEEDED names, because it is dlopen'ed, is traced explicitly: glibc's
# NSS plugins here, and the caller's EXTRA_DLOPEN (the Intel runtime).
set -eu
dst=$1
trace() {
	for f in "$@"; do
		[ -e "$f" ] || { echo "stage.sh: $f does not exist" >&2; exit 1; }
		lddtree --copy-to-tree "$dst" "$f" >/dev/null
		# A soname symlink (libva.so.2) is what the loader and libvpl ask
		# for; keep it beside the file lddtree copied.
		if [ -L "$f" ]; then
			mkdir -p "$dst$(dirname "$f")"
			cp -a "$f" "$dst$f"
		fi
	done
}
multiarch=$(gcc -print-multiarch 2>/dev/null || dpkg-architecture -qDEB_HOST_MULTIARCH)
sys=/usr/lib/$multiarch

trace /usr/bin/squasharr-worker /usr/bin/ffmpeg /usr/bin/ffprobe /usr/lib/libffshim.so
for f in /usr/lib/libav*.so.* /usr/lib/libsw*.so.* /usr/lib/libpostproc.so.*; do
	[ -e "$f" ] && [ ! -L "$f" ] && trace "$f"
done
# lddtree copies real files; the loader asks for the sonames.
for f in /usr/lib/lib*.so.*; do
	[ -L "$f" ] && cp -a "$f" "$dst/usr/lib/"
done
trace "$sys"/libnss_dns.so.2 "$sys"/libnss_files.so.2 "$sys"/libresolv.so.2
# shellcheck disable=SC2086
[ -z "${EXTRA_DLOPEN:-}" ] || trace $EXTRA_DLOPEN

mkdir -p "$dst/etc/ssl/certs" "$dst/data" "$dst/scratch" "$dst/tmp"
cp /etc/ssl/certs/ca-certificates.crt "$dst/etc/ssl/certs/"
echo 'hosts: files dns' >"$dst/etc/nsswitch.conf"
printf 'root:x:0:0:root:/root:/sbin/nologin\nclustarr:x:1000:1000::/nonexistent:/sbin/nologin\n' >"$dst/etc/passwd"
printf 'root:x:0:\nclustarr:x:1000:\n' >"$dst/etc/group"
chown 1000:1000 "$dst/data" "$dst/scratch"
chmod 1777 "$dst/tmp"
