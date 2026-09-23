/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Fingerprint is a deterministic 16-hex-char sha256 prefix of
// TitleNorm+Year+Seasons+Episodes+Quality.Name+Group, stable across two
// indexers describing the same content. It is a content-level dedup key,
// distinct from indexarr's own sha1(indexer:guid) Msg-Id (spec §8.7): that
// one dedupes repeat sightings of the *same* indexer/guid pair; this one
// dedupes the *same release* seen through two different indexers, which
// neither guid nor infohash can do when only one of the two carries an
// infohash (a usenet mirror of a scene release, for example).
//
// The title half is TitleNorm, not CleanTitle: CleanTitle reduces every
// wholly non-Latin title to "", so two different films released the same
// year at the same quality by the same group would share one fingerprint
// and one would be deduplicated away as a copy of the other.
func (p *ParsedRelease) Fingerprint() string {
	var b strings.Builder
	b.WriteString(TitleNorm(p.Title))
	fmt.Fprintf(&b, "|%d", p.Year)
	fmt.Fprintf(&b, "|%v|%v", p.Seasons, p.Episodes)
	b.WriteString("|")
	b.WriteString(p.Quality.Name)
	b.WriteString("|")
	b.WriteString(strings.ToLower(p.Group))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}
