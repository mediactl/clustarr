# A movie released in parts across releases (part sets)

Status: approved by the owner 2026-10-16 ("build the feature"); builds on
the within-one-download join (ecb1dd8e).

## Problem

Some films exist on the indexers only as separately posted parts:
*56 Up* as `56.Up.2012.Part1/2/3.1080p.BluRay.x264-USURY` (three posts),
*No Direction Home* as `...Part.1/Part.2...x264-RedBlade`. Each part is its
own release, so each is judged, grabbed and imported as if it were the
film: the decision rejects it `BelowMinimumSize`, import refuses it as a
part (under 65% of the runtime), and before those rules one part was
imported as the movie. The within-download join (`importplan.foldParts`)
only joins parts that arrive in one payload.

## Design

### 1. A set is one search result

`release.PartOf(title)` reads a part token (Part1, Part.2, Pt3, CD1,
Disc 2) and returns the title without it. `decision.PartSets`, run on a
movie search's decisions (the search worker, so the automatic grab and an
interactive Search both see it), groups the releases whose part token comes
after the parsed title -- "Che.Part.1.2008" is a title, not a part -- by
indexer, parsed title, year, quality, release group (case folded) and
edition. A group is a set when it holds parts 1 to N, each once after
choosing the best post of a part (the fewest rejections other than size,
then the newest), with 2 <= N <= `commonv1.MaxReleaseParts` (6).

A set becomes one release: its title the members' without the part token,
its size their sum, its GUID `parts:` and 24 hex digits of the SHA-256 of
the members' GUIDs (stable across searches), `Parts` its members in order.
It is evaluated by the same evaluator as any release -- so the size rules
judge the whole film -- and carries every member's rejections other than
the size ones; a member blocked in the release index blocks it. The
members stay in the results as they were. Only movies.

### 2. Grabbing a set grabs every part

A set is a candidate like any other: the automatic search grabs it when it
ranks best, and a person picks it in the interactive search with one click
(it is one row, `Parts` listing what it fetches). The grab planner makes
one entry per part in the same pass, each carrying
`status.downloads[].set = {id, part, of}` and its own release and source
(resolved from the part). A set is never delayed or held for a grab limit
-- it is grabbed now or refused -- and needs room for all its entries under
the owner's cap. Every guard that refuses a second grab of a movie sees the
set's entries as its live grabs.

### 3. Each part downloads as an ordinary transfer

Nothing in the engines changes.

### 4. The set imports once, joined

Part 1 is the set's lead. The downloads stage decides the lead's import
only once every part has completed, and gives importplan the other parts'
entries and transfer records. The inspect task names each part's root
(`ImportInspectTask.Parts`); the import agent walks every root and tags
each file with its part (`InspectedFile.Part`); the planner folds the
movie's files as the within-download join does, numbering a file by its
tag when its name carries no part. A set's import must join all N parts,
or it is a release fault; any joined movie must run at least 85% of the
film's runtime (`importplan.JoinedShortPercent`).

The other parts wait at Completed and follow the lead: Imported when it
imports (then removed with their data, as an imported entry is), removed
when it fails, is blocklisted or its hold expires. A part that fails while
the others download or wait has them removed; it is itself failed or
blocklisted as ever, which owes the usual redownload search.

### 5. Unchanged

The movie reads Downloading throughout (every part is live until the
import), so neither the wanted sweep nor the transcode planner acts on it
meanwhile. History, the UI's download rows and the engines see N ordinary
entries.

## Out of scope

Episodes; sets mixing indexers (each indexer's posts form their own set);
torrent sets are allowed but untested on the owner's indexers.
