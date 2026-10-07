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

package importplan

import (
	"fmt"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// The summary messages lifecycle and the ui read (moved from api/download,
// ADR-0019 §6.9).
const (
	// MessageEveryFileRejected is an import refused as the release's fault.
	MessageEveryFileRejected = "every candidate file was rejected"
	// MessageExistingFileFinal is an import refused because the item's file
	// is transcoded: the item's state, never the release's fault.
	MessageExistingFileFinal = "the existing file is transcoded, and a transcoded file is final: " +
		"import this download by hand to replace it"
)

// overrideHint is how a person takes a held import: the import intent with
// override, naming the item when the target is the question.
const overrideHint = "download.clustarr.io/import with override (and target=<kind>/<name>[/<key>] to name the item)"

// outcomeMessage is the summary's message for a plan that imports nothing,
// before conclude says what happens next. A plan with no rejection at all
// says noneMessage.
func outcomeMessage(rs []Rejection, noneMessage string) string {
	if len(rs) == 0 {
		return noneMessage
	}
	switch Classify(rs) {
	case commonv1.ImportClassTransient:
		return "a file could not be read, probed or placed"
	case commonv1.ImportClassNeedsPerson:
		return "a file cannot be attributed or placed without a person: import it by hand (" + overrideHint + ")"
	case commonv1.ImportClassItemState:
		for _, r := range rs {
			if r.Transcoded {
				return MessageExistingFileFinal
			}
		}
		return "the item does not take these files now"
	default:
		return MessageEveryFileRejected
	}
}

// verdictMessage renders a non-Upgrade quality.Verdict as a rejection
// reason.
func verdictMessage(v quality.Verdict) string {
	switch v {
	case quality.ExistingBetterQuality:
		return "the existing file already has a better quality"
	case quality.UpgradesNotAllowed:
		return "the quality profile does not allow upgrades"
	case quality.ExistingBetterRevision:
		return "the existing file already has a better proper/repack revision"
	case quality.QualityCutoffMet:
		return "the existing file already meets the quality profile's cutoff"
	case quality.FormatScoreNotHigher:
		return "the candidate's custom-format score is not higher than the existing file's"
	case quality.FormatCutoffMet:
		return "the existing file already meets the quality profile's custom-format cutoff"
	case quality.FormatIncrementTooSmall:
		return "the candidate's custom-format score improvement is below the profile's minimum increment"
	default:
		return fmt.Sprintf("not an upgrade over the existing file (verdict %d)", v)
	}
}

// notAllowedRejection is a file whose (probe-corrected) quality the profile
// does not allow: the grab approved the release's advertised quality, so
// the file is the release's fault (P57).
func notAllowedRejection(rel string, q commonv1.Quality) Rejection {
	return releaseFaultRejection("%s: quality %s is not allowed by the quality profile", rel, q.Name)
}

// filledRejection is a candidate whose item a better file of the same
// download already filled: incidental.
func filledRejection(rel, item, by string) Rejection {
	return incidentalRejection("%s: %s already has a file from this download, %s, "+
		"which ranks higher (quality, then revision, then size)", rel, item, by)
}
