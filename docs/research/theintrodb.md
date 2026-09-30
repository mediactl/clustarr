# TheIntroDB API (v3)

Verified live on 2026-09-30. Used by `pkg/metadata/clients/theintrodb`. The
fixtures are in `test/data/metadata/theintrodb/`.

## Request

`GET https://api.theintrodb.org/v3/media` takes:

- exactly one of `tmdb_id`, `imdb_id` or `tvdb_id`;
- `season` and `episode` for a TV episode;
- `duration_ms` (optional): the file's duration, which picks the release.

A TVDB id is resolved to the title's TMDB id: `tvdb_id=81189` answers with `tmdb_id: 1396`.

## Response

```json
{"tmdb_id":1396,"type":"tv","season":1,"episode":1,
 "intro":[{"start_ms":228664,"end_ms":246143}],
 "credits":[{"start_ms":3431000,"end_ms":null}]}
```

There are four optional arrays: `intro`, `recap`, `credits` and `preview`. Each holds `{start_ms, end_ms}` pairs:

- a null `start_ms` means from the start of the file;
- a null `end_ms` means to the end of the file;
- `{"start_ms":null,"end_ms":0}` means "none" (Friends S01E01's intro).

A title the service doesn't have returns 404 `{"error":"media not found"}`.

## Limits

These are the anonymous limits, read from the response headers:

- `x-ratelimit-limit: 30` per `x-ratelimit-reset: 10` seconds;
- `x-usagelimit-limit: 500`;
- `x-usagelimit-specificmedia-limit: 2000`.

An exhausted allowance is a 429 with no `Retry-After` and no body worth
reading. Recorded on 2026-09-30:

- `x-usagelimit-remaining: 0`;
- `x-usagelimit-reset: 6945`, the seconds until the allowance returns.

The client waits for the reset of whichever limit reads 0 remaining. A
throttled Ping marks the MetadataProvider `Throttled` and not Ready.

An `Authorization` header is accepted; the client sends a key as `Bearer <key>`.

## Coverage

On 2026-09-30:

- present: Breaking Bad, Game of Thrones, The Office, Friends, The Matrix;
- absent (404): Andor, Wolfwalkers.
