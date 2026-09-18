# ADR-0002: The project is licensed GPL-3.0

**Status:** Accepted, 2026-09-18

## Context

Clustarr's value is not in inventing release parsing, quality decision logic, naming grammars or
subtitle scoring. Those bodies of knowledge already exist, encoded over a decade in Radarr,
Sonarr, Lidarr, Readarr, Prowlarr and Bazarr: the release-title regex sets and their ordering, the
custom-format specification model, the decision-engine's ordered specification checks, the naming
token grammar and its per-media-server presets, Bazarr's subtitle scoring weights and its
hearing-impaired detection regexes. Reimplementing them "from the docs" produces subtly different
behaviour in exactly the cases users notice — anime absolute numbering, daily shows, season packs,
multi-episode files, forced/SDH subtitle tracks.

Every one of those projects is GPL-3.0. A port of a regex table or a scoring function is a
derivative work of it. So the licence is not a values statement here; it is a precondition for the
implementation strategy.

## Decision

Clustarr is GPL-3.0-or-later. Every Go file carries the header in `hack/boilerplate.go.txt`, and
the full text is in `LICENSE`. Logic may be ported from the *arr projects and Bazarr, with the
origin noted at the port site so provenance stays auditable.

Data is a separate question from code. TRaSH Guides data — the custom-format definitions, the
scores, the quality-profile recommendations we ship as our opinionated subset — is MIT, so it can
be vendored and redistributed without further constraint, and is marked as such where it is
bundled.

## Alternatives considered

**Apache-2.0.** The default for Kubernetes-ecosystem Go projects, with an explicit patent grant
and the widest downstream reach — vendors could embed Clustarr in a closed product. It is
incompatible with the plan: under Apache-2.0 we would have to clean-room every ported table and
still carry the risk that the result is legally a derivative. The cost of that reimplementation is
not weeks of typing, it is years of accumulated edge cases we would not have.

**MIT / BSD.** Same defect as Apache-2.0, minus the patent grant.

**AGPL-3.0.** Compatible with the same ports and closes the hosting loophole. Rejected because
Clustarr is self-hosted infrastructure, not a service: the network clause protects against a case
that barely exists here, while measurably deterring contributors and operators who have blanket
policies against AGPL software in their clusters.

**Dual licence / open core.** Impossible without owning the copyright on everything, which we do
not once upstream logic is ported.

## Consequences

Derivative works and redistributed forks must also be GPL-3.0, which rules out Clustarr being
embedded in a proprietary product. That is accepted; it is not a market we are serving.
Contributors license their work under the same terms, and we do not require a CLA, so relicensing
later would require consent from everyone who has contributed — the licence should be treated as
permanent.

More practically: any new dependency must be GPL-3.0-compatible, which excludes anything under
older GPL-incompatible licences (for example the original OpenSSL licence, or SSPL-covered
components). Vendored data must have its own licence recorded. And the boilerplate header is
enforced mechanically by the generators and lint config rather than by review, so it cannot drift.

## Revisit triggers

Only one realistic trigger: if all ported logic were removed — if Clustarr's parsers, decision
engine, naming and subtitle scoring became independent implementations with no upstream
derivation — the constraint would lift and a permissive licence would become possible. Given that
copyright on contributions is distributed and no CLA is in place, that path also requires
consent from every contributor, so in practice this decision stands for the life of the project.
