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

package subtitlerequest_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/controller/subtitlerequest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// An import list's removeAndKeep deletes the Movie and keeps the file and
// its MediaFile (gap-fix X7b), so the SubtitleRequest the MediaFile owns
// survives. From a steady state -- both managers' status on it, a language
// due -- the request goes Blocked (ItemNotFound): no fetch task, and the
// Blocked apply re-declares everything, so nothing is released. A list
// re-adding the Movie lifts the block and the schedule carries on.
func TestARequestWhoseItemIsGoneIsBlockedAndPublishesNothing(t *testing.T) {
	f := newFixture(t, "sr-removeandkeep")
	before := steadyState(t, f)
	f.clock.Advance(7 * 24 * time.Hour) // de is long overdue
	published := len(f.bus.calls())

	require.NoError(t, f.c.Delete(f.ctx, &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "movie", Namespace: f.ns}}))
	res := f.reconcile("movie")
	after := f.get("movie")

	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseBlocked, after.Status.Phase)
	planned := cond(after, subtitlev1alpha1.SubtitleRequestConditionPlanned)
	assert.Equal(t, metav1.ConditionFalse, planned.Status)
	assert.Equal(t, subtitlerequest.ReasonItemNotFound, planned.Reason)
	assert.Contains(t, planned.Message, "removeAndKeep")
	assert.Len(t, f.bus.calls(), published, "no subtitle is fetched for a file Clustarr no longer manages")
	assert.Zero(t, res.RequeueAfter, "the Movie watch lifts the block; nothing polls")

	assert.Equal(t, before.Status.Existing, after.Status.Existing, "the block released existing")
	assert.Equal(t, before.Status.ProbeHash, after.Status.ProbeHash, "the block released probeHash")
	assert.Equal(t, item(t, before, "de"), item(t, after, "de"), "the block changed an item")
	assertManagedFieldsSplit(t, after,
		map[k8s.FieldManager][]string{k8s.ManagerCaptionarr: controllerTop, k8s.ManagerCaptionarrWorker: {"items"}},
		map[k8s.FieldManager]map[string][]string{
			k8s.ManagerCaptionarr:       {"de": controllerItem},
			k8s.ManagerCaptionarrWorker: {"de": workerLeaves},
		})

	// A list re-adds the film under the same name: the kept MediaFile is
	// managed again, and the overdue language goes out.
	f.movie("movie")
	f.reconcile("movie")
	again := f.get("movie")
	assert.NotEqual(t, subtitlev1alpha1.SubtitleRequestPhaseBlocked, again.Status.Phase)
	assert.Equal(t, metav1.ConditionTrue, cond(again, subtitlev1alpha1.SubtitleRequestConditionPlanned).Status)
	require.Len(t, f.bus.calls(), published+1, "the schedule carries on once the item is back")
	assert.Equal(t, "de", f.bus.calls()[published].task.LangKey)
}
