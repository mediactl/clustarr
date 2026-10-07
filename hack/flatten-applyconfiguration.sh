#!/usr/bin/env bash
# hack/flatten-applyconfiguration.sh
#
# controller-gen v0.22.0 writes a group's apply configurations to
# <+kubebuilder:ac:output:package>/<group>/<version>, so the per-group output
# packages (api/applyconfiguration/<group>) end up holding
# api/applyconfiguration/<group>/<group>/<version>. Pointing every group at the
# shared root instead is no fix: each group then writes the one shared
# internal/internal.go and the last group's schema overwrites the others', so
# Extract* on every other group fails at runtime. This moves each
# <group>/<group>/<version> up to <group>/<version> and rewrites every Go import
# of the old path. It is idempotent; `make generate` runs it after controller-gen.
set -euo pipefail

root=api/applyconfiguration
mod=$(go list -m)

for gdir in "$root"/*/; do
	g=$(basename "$gdir")
	[ -d "$root/$g/$g" ] || continue
	for vdir in "$root/$g/$g"/*/; do
		v=$(basename "$vdir")
		rm -rf "${root:?}/$g/$v"
		mv "$vdir" "$root/$g/$v"
	done
	rmdir "$root/$g/$g"
	git grep -l --untracked -F "$mod/$root/$g/$g/" -- '*.go' |
		xargs -r sed -i "s#$mod/$root/$g/$g/#$mod/$root/$g/#g"
done
