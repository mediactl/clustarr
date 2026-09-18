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

package subtitles

import (
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// Query is spec §7's exact struct. Per the controller amendment (wave 2,
// pkg/release has landed), Release is the real *release.ParsedRelease
// rather than a local placeholder — the future search worker fills it in
// from the parsed release of the local file being searched for.
type Query struct {
	Kind            common.MediaKind
	Title           string
	Year            int
	IDs             map[string]string // "imdb", "tmdb", "parent_imdb", "parent_tmdb" — no "tt"/prefix, per OpenSubtitles
	Season, Episode int
	Hash            string // moviehash, 16 lowercase hex; caller-supplied — pkg/mediainfo.MovieHash (spec §7) owns computation, this package never calls it
	SizeBytes       int64
	Release         *release.ParsedRelease
	Languages       []LangKey
}

// Candidate is spec §7's exact struct plus additive provider-search
// metadata (Uploader..FetchID) needed to implement Search/Download and
// Bazarr's trusted_sources/ai_translated filters — no spec-declared field
// is renamed or dropped.
type Candidate struct {
	Provider, ID, Language  string
	HI, Forced              bool
	ReleaseInfo             string
	Matches                 map[string]bool
	Score, ScoreWithoutHash int

	Uploader          string
	Trusted           bool
	AITranslated      bool
	MachineTranslated bool
	Downloads         int
	FetchID           string // provider-scoped fetch handle, e.g. OpenSubtitles file_id
}
