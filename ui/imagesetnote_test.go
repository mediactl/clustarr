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

package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/views"
)

// The item page's Images fact names the set in use: the owner's pasted one
// first (it wins wherever both are read), else the mediux provider's
// choice with a link to its page, else nothing.
func TestTheImagesFactNamesTheSetInUse(t *testing.T) {
	chosen := &catalogv1.ImageSetStatus{
		Provider: "mediux", SetID: "41471", Title: "Cyberpunk: Edgerunners", Creator: "sharivari",
		URL: "https://mediux.pro/sets/41471", CheckedAt: metav1.Now(),
	}
	assert.Equal(t, &views.ImageSetNote{Label: "MediUX set Cyberpunk: Edgerunners by sharivari", Href: "https://mediux.pro/sets/41471"},
		imageSetNote(nil, nil, chosen))
	assert.Equal(t, &views.ImageSetNote{Label: "pasted set", Pasted: true},
		imageSetNote([]catalogv1.ArtworkOverride{{Type: catalogv1.ImageTypePoster, URL: "https://x/p"}}, nil, chosen))
	composed := chosen.DeepCopy()
	composed.URL = "https://mediux.io/show-set/41471"
	composed.Sets = []catalogv1.ImageSetSource{{SetID: "41471", Images: 14}, {SetID: "22523", Images: 10}, {SetID: "21971", Images: 1}}
	assert.Equal(t, &views.ImageSetNote{Label: "MediUX set Cyberpunk: Edgerunners by sharivari and 2 more sets", Href: "https://mediux.io/show-set/41471"},
		imageSetNote(nil, nil, composed), "the anchor named, the sets that filled its gaps counted")
	composed.Sets = composed.Sets[:2]
	assert.Equal(t, "MediUX set Cyberpunk: Edgerunners by sharivari and 1 more set", imageSetNote(nil, nil, composed).Label)
	assert.Nil(t, imageSetNote(nil, nil, &catalogv1.ImageSetStatus{NotFound: true, CheckedAt: metav1.Now()}))
	assert.Nil(t, imageSetNote(nil, nil, nil))
}
