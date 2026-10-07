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

import common "github.com/mediactl/clustarr/api/common/v1alpha1"

// ResultBand is where a release sits in an interactive search's results
// (series search design §8.2): approved releases first, then rejected
// ones, then the releases for other titles, which the ui collapses.
type ResultBand int

// The bands, in their order.
const (
	BandApproved ResultBand = iota
	BandRejected
	// BandOtherTitle is any release with a WrongItem, UnknownItem or
	// UnableToParse rejection: identity noise, kept so a person can still
	// override a mislabelled release.
	BandOtherTitle
)

// BandOf is a release's band: approved; else other-title when any
// rejection is WrongItem, UnknownItem or UnableToParse; else rejected. An
// item-state rejection is never an identity one, so it moves a release
// from approved to rejected, never into other titles.
func BandOf(approved bool, rejs []common.Rejection) ResultBand {
	if approved && len(rejs) == 0 {
		return BandApproved
	}
	for _, r := range rejs {
		if ReasonWrongItem.Of(r) || ReasonUnknownItem.Of(r) || ReasonUnableToParse.Of(r) {
			return BandOtherTitle
		}
	}
	return BandRejected
}

// String is the band's metric label: "approved", "rejected" or
// "otherTitles".
func (b ResultBand) String() string {
	switch b {
	case BandApproved:
		return "approved"
	case BandRejected:
		return "rejected"
	default:
		return "otherTitles"
	}
}
