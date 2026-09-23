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

package decision

import (
	"context"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// originalLanguageName is the ONE place a catalog item's original language
// crosses from the vocabulary the CRD stores into the vocabulary the release
// pipeline speaks.
//
// The two sides genuinely disagree, and both are right on their own terms:
//
//   - Movie.status.metadata.originalLanguage and its Series twin are BCP-47
//     tags ("en", "ja", "pt-BR") -- the vocabulary every metadata provider
//     returns and the only one that round-trips through an API.
//   - release.ParsedRelease.Languages and every CondLanguage condition in the
//     TRaSH catalogue are Radarr's English display names ("English",
//     "Japanese") -- the vocabulary a release TITLE is parsed into.
//
// pkg/decision is where the two meet: Evaluate is the only production caller
// that reads Target's original language and the only production producer of
// catalogue.ItemContext, so converting here covers both consumers at once and
// leaves no call site with a rule to remember. Callers hand over the CRD
// field verbatim (Target.OriginalLanguageTag) and cannot get it wrong; the
// field's name is the contract.
//
// A tag the table cannot resolve, and an absent tag, both resolve to "" --
// which every consumer reads as "this item's original language is UNKNOWN",
// not as "the original language is the empty string". That distinction is the
// whole point: an unknown language must not turn into a rejection. See
// languageRejection's "original" branch and catalogue.evalCondition's
// CondLanguage case, which each fail open on "".
func originalLanguageName(ctx context.Context, tag string) string {
	if tag == "" {
		// No metadata yet, or a provider with nothing to say. Normal, and
		// not worth a log line on every release of every item.
		return ""
	}
	name, ok := catalogue.LanguageName(tag)
	if !ok {
		// Worth saying out loud: the item is permanently outside the
		// "original language" machinery until the tag is understood, and
		// before this log the only symptom was a decision that quietly
		// stopped constraining language. One line per Evaluate call (not
		// per release), because Evaluate resolves this once.
		logging.FromContext(ctx).Warn(
			"decision: original language tag is not in the language table; treating the item's original language as unknown",
			"tag", tag)
		return ""
	}
	return name
}
