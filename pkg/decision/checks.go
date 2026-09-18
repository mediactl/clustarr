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
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// protocolRejection is ProtocolSpecification: a protocol absent from
// o.ProtocolsEnabled is treated as disabled. The caller populates both
// "torrent" and "usenet" from the resolved DelayProfile's
// EnableUsenet/EnableTorrent (default true on the CRD), so an absent key in
// practice means "no DelayProfile resolved yet," which should fail closed.
func protocolRejection(rel common.ReleaseInfo, o Options) *common.Rejection {
	if o.ProtocolsEnabled[string(rel.Protocol)] {
		return nil
	}
	r := newRejection(ReasonProtocolDisabled, "%s is not enabled", rel.Protocol)
	return &r
}

// availabilityRejection is RssSync/AvailabilitySpecification: skipped
// entirely for a user-invoked (interactive) search.
func availabilityRejection(t Target, o Options) *common.Rejection {
	if o.UserInvoked || t.Available {
		return nil
	}
	r := newRejection(ReasonUnavailable, "item is not yet available")
	return &r
}

// qualityRejections is QualityAllowedByProfileSpecification +
// CustomFormatAllowedByProfileSpecification: both run unconditionally (a
// release can fail either or both at once, matching the real
// DownloadDecision's accumulate-every-rejection behavior -- Disagreement 6).
func qualityRejections(p quality.Profile, rel common.ReleaseInfo, score int) []common.Rejection {
	var out []common.Rejection
	if !p.Allowed(rel.Quality) {
		out = append(out, newRejection(ReasonQualityNotWanted, "%s is not wanted in this profile", rel.Quality.Name))
	}
	if score < p.MinFormatScore {
		out = append(out, newRejection(ReasonCustomFormatMinimumScore,
			"custom format score %d is below the profile minimum %d", score, p.MinFormatScore))
	}
	return out
}

// languageRejection is LanguageSpecification, using Profile.LanguageName --
// go doc ./pkg/quality: "'original' and 'any' pass through unchanged, an
// empty Language stays empty (no constraint)".
func languageRejection(t Target, p quality.Profile, parsed *release.ParsedRelease) *common.Rejection {
	switch p.LanguageName {
	case "", "any":
		return nil
	case "original":
		if containsFold(parsed.Languages, t.OriginalLanguage) {
			return nil
		}
		r := newRejection(ReasonWantedLanguage, "original language %s is wanted, but found %v", t.OriginalLanguage, parsed.Languages)
		return &r
	default:
		if containsFold(parsed.Languages, p.LanguageName) {
			return nil
		}
		r := newRejection(ReasonWantedLanguage, "%s is wanted, but found %v", p.LanguageName, parsed.Languages)
		return &r
	}
}

func containsFold(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
