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

package search

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/intake"
)

// A pick its owner refused is reported with the planner's reason, read
// from the inbox under the pick's own Msg-Id (which spec.override changes);
// without one the error says only that it was not taken, and never points
// at Events the owner does not have.
func TestARefusedPickIsReportedWithThePlannersReason(t *testing.T) {
	const guid = "https://indexer.example/a"
	s := &catalogv1alpha1.Search{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "pick", UID: "search-uid"}}
	s.Status.Results = []commonv1.ReleaseDecision{{ReleaseInfo: commonv1.ReleaseInfo{GUID: guid, IndexerRef: "example"}}}
	r := &Reconciler{Inbox: intake.NewInbox(4)}
	const why = "QueueHigherPreference: the release covers an episode a grab of equal or higher preference is already downloading"
	r.Inbox.SettleBecause(pickMsgID(s, "example", guid), intake.Refused, why)

	assert.Equal(t, "the item's owner did not take the release: "+why, refusedText(r.refusal(s, guid)))

	s.Spec.Override = true
	assert.Equal(t, "the item's owner did not take the release", refusedText(r.refusal(s, guid)), "the override's pick is another candidate")
	assert.Empty(t, (&Reconciler{}).refusal(s, guid))
	assert.NotContains(t, refusedText(""), "Events")
	assert.LessOrEqual(t, len(refusedText(strings.Repeat("x", 2000))), maxGrabError)
}
