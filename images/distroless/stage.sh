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
		# for: keep it, and the file it names, side by side.
		if [ -L "$f" ]; then
			real=$(readlink -f "$f")
			mkdir -p "$dst$(dirname "$f")" "$dst$(dirname "$real")"
			cp -a "$real" "$dst$real"
			cp -a "$f" "$dst$f"
		fi
	done
}
multiarch=$(gcc -print-multiarch 2>/dev/null || dpkg-architecture -qDEB_HOST_MULTIARCH)
sys=/usr/lib/$multiarch

# Debian merges /lib into /usr/lib, and ldd reports either spelling; the
# staged tree is merged the same way, so a library copied as
# /lib/<multiarch>/x and one looked for as /usr/lib/<multiarch>/x are one
# file. (Unmerged, the dispatcher that looks in /usr/lib found only a
# dangling soname link to a file staged under /lib.)
mkdir -p "$dst/usr/lib" "$dst/usr/lib64" "$dst/usr/bin"
ln -sfn usr/lib "$dst/lib"
ln -sfn usr/lib64 "$dst/lib64"

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

# Notices: the copyright file of every Debian package a staged file came
# from (glibc, libstdc++, libva, the iHD driver, ...), and the licences the
# build stages put in /usr/share/licenses (FFmpeg and its source, ffgo,
# clustarr).
find "$dst" -type f | while read -r f; do
	p=${f#"$dst"}
	pkg=$(dpkg -S "$p" 2>/dev/null || dpkg -S "/usr${p#/usr}" 2>/dev/null || dpkg -S "${p#/usr}" 2>/dev/null || true)
	pkg=${pkg%%:*}
	pkg=${pkg%%,*}
	[ -n "$pkg" ] && [ -f "/usr/share/doc/$pkg/copyright" ] || continue
	mkdir -p "$dst/usr/share/doc/$pkg"
	cp "/usr/share/doc/$pkg/copyright" "$dst/usr/share/doc/$pkg/copyright"
done
mkdir -p "$dst/usr/share"
cp -r /usr/share/licenses "$dst/usr/share/"

mkdir -p "$dst/etc/ssl/certs" "$dst/data" "$dst/scratch" "$dst/tmp"
cp /etc/ssl/certs/ca-certificates.crt "$dst/etc/ssl/certs/"
echo 'hosts: files dns' >"$dst/etc/nsswitch.conf"
printf 'root:x:0:0:root:/root:/sbin/nologin\nclustarr:x:1000:1000::/nonexistent:/sbin/nologin\n' >"$dst/etc/passwd"
printf 'root:x:0:\nclustarr:x:1000:\n' >"$dst/etc/group"
chown 1000:1000 "$dst/data" "$dst/scratch"
chmod 1777 "$dst/tmp"
