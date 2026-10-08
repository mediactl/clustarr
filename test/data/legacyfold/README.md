# Legacy-fold fixtures (F8.4 step 1c)

`segments-v1.json.gz` is the owner-approved, read-only dump of the live
`clustarr-segments` bucket taken 2026-10-07
(`.superpowers/unify/fixtures/clustarr-segments-2026-10-07.jsonl.gz`, 11,967 JSON
lines of `{key, revision, value}`), reshaped as F8.4 step 1c names it: one JSON
object `{"<MediaFile UID>": <record>}`, sorted by UID, gzipped without a name or
mtime. Each key is the bucket key unescaped (`events.RecordKey` is
`events.KVKeyToken(uid)`, which doubles a UID's `-`); each value is the previous
release's v1 `segments.Record` as stored (9,741 `Found`, 2,226 `NotFound`; 11,964
at analyzer version 3, three at 2). The bucket's revisions are not kept.

The whole bucket is kept, not step 1c's 64-UID cut: the cut is chosen by
`seedkey-mediafiles.json.gz` (step 1b, 64 MediaFiles from a `kubectl get
mediafiles -o json` dump), which no one has taken yet. Until it exists,
`TestTheFirstLoopPassKeepsEverySeedKey` (app/remediation/markers) has no
MediaFiles to plant.
