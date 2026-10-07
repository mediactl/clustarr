#!/usr/bin/env bash
# hack/brand-gate.sh -- the Wave U gate (docs/superpowers/specs/2026-10-07-unbranded-names-design.md
# §7, as superseded by the owner's cold-turkey swap of 2026-10-07): prints every line that names a
# retired *arr brand (or the derived app directory) outside the historical records, and exits 1 if
# there is one. The pattern below spells no brand, so this file does not match itself.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

re='(catalog|import|index|grab|caption|segment)arr|squas[h]'
readme=charts/clustarr/README.md

# Temporary: the per-service installer set W6.9-W6.15 delete or rewrite. W6.17 deletes this list.
temp=(
  ':(exclude)charts/clustarr'
  ':(exclude)config/manager'
  ':(exclude)config/rbac'
  ':(exclude)config/prometheus'
  ':(exclude)config/postgres'
  ':(exclude)config/e2e'
  ':(exclude)config/README.md'
  ':(exclude)Makefile'
  ':(exclude)hack/e2e.sh'
)

# The chart README's "Upgrading from 0.4.x" section names the retired values keys on purpose.
lo=0 hi=0
if [[ -f $readme ]]; then
  read -r lo hi < <(awk '/^## Upgrading from 0\.4\.x/ { s = NR; next }
    s && /^## / { print s, NR - 1; found = 1; exit }
    END { if (s && !found) print s, NR; else if (!s) print 0, 0 }' "$readme")
fi

{ git grep -n -I -i -E "$re" -- . \
    ':(exclude)docs/superpowers' \
    ':(exclude)docs/research' \
    ':(glob,exclude)docs/adr/[0-9]*.md' \
    ':(exclude)docs/runbooks/unify-cold-cutover.md' \
    ${temp[@]+"${temp[@]}"} || true; } |
awk -v readme="$readme" -v lo="$lo" -v hi="$hi" -v re="$re" '
  {
    path = $0; sub(/:.*/, "", path)
    rest = substr($0, length(path) + 2)
    line = rest; sub(/:.*/, "", line)
    text = tolower(substr(rest, length(line) + 2))
    if (path == readme && line + 0 >= lo + 0 && line + 0 <= hi + 0 && lo + 0 > 0) next
    # Citations of historical files whose own names carry a brand.
    gsub(/docs\/research\/phase-d1-index[a]rr-spec\.md/, "", text)
    gsub(/docs\/superpowers\/plans\/2026-09-19-phase-d1-index[a]rr\.md/, "", text)
    gsub(/docs\/superpowers\/plans\/2026-09-22-phase-d2-gra[b]arr\.md/, "", text)
    gsub(/\.superpowers\/sdd\/2026-09-22-phase-d2-gra[b]arr\//, "", text)
    if (text ~ re) { print; n++ }
  }
  END {
    if (n) { printf "brand-gate: %d line(s) name a retired brand outside the records\n", n > "/dev/stderr"; exit 1 }
  }'
