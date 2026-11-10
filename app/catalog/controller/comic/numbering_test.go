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

package comic_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
)

// VolumeNumber reads the volume a collected edition's number or title
// spells out, the number first, leading zeroes dropped; a bare number
// spells no volume.
func TestVolumeNumber(t *testing.T) {
	cases := []struct {
		name, number, title string
		want                string
		ok                  bool
	}{
		{"Vol. with a dot", "Vol. 3", "", "3", true},
		{"Volume spelled out", "Volume 12", "", "12", true},
		{"vNN", "v03", "", "3", true},
		{"Vol without a dot, leading zeroes", "Vol 007", "", "7", true},
		{"Tome", "Tome 4", "", "4", true},
		{"Book", "Book 2", "", "2", true},
		{"the number first, then the title", "Vol. 5", "Volume 9", "5", true},
		{"the title when the number spells none", "TPB", "Chainsaw Man, Vol. 3", "3", true},
		{"the title in brackets", "", "Berserk (Volume 11)", "11", true},
		{"a title naming a volume then more", "", "Vol. 2: The Second Arc", "2", true},
		{"a bare number spells no volume", "3", "", "", false},
		{"a word starting with v is no volume", "", "Vampire 3", "", false},
		{"a title with the word but no number", "", "The Volumes", "", false},
		{"nothing", "", "", "", false},
		{"Vol. 0", "Vol. 0", "", "0", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := comic.VolumeNumber(c.number, c.title)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.want, got)
		})
	}
}

// VolumeIssueNumber renumbers only under volumeAsIssue, and only an issue
// that spells a volume.
func TestVolumeIssueNumber(t *testing.T) {
	vai := catalogv1alpha1.ComicSpec{SpecialVersion: catalogv1alpha1.ComicSpecialVersionVolumeAsIssue}
	assert.Equal(t, "3", comic.VolumeIssueNumber(vai, "Vol. 3", ""))
	assert.Equal(t, "3", comic.VolumeIssueNumber(vai, "TPB", "Vol. 3"))
	assert.Equal(t, "7", comic.VolumeIssueNumber(vai, "7", "Volume 7"), "a plain number passes through")
	assert.Equal(t, "HC", comic.VolumeIssueNumber(vai, "HC", "Deluxe"), "a number spelling no volume passes through")
	for _, sv := range []catalogv1alpha1.ComicSpecialVersion{"", catalogv1alpha1.ComicSpecialVersionNormal, catalogv1alpha1.ComicSpecialVersionTPB, catalogv1alpha1.ComicSpecialVersionOmnibus} {
		assert.Equal(t, "Vol. 3", comic.VolumeIssueNumber(catalogv1alpha1.ComicSpec{SpecialVersion: sv}, "Vol. 3", ""), string(sv))
	}
}
