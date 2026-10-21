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

package lifecycle_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// An entry assigned to a DownloadClient that was deleted before its
// transfer started was owed to the gone client's engine for good: three
// Naked and Afraid episodes sat Assigned to usenet-local, re-sent every 50
// minutes to usenet-local-0 for days after the client was deleted (talos,
// 2026-10-20). Such an entry is assigned again, to an enabled client of its
// protocol, and its add goes there.
func TestAnAssignedEntryWhoseClientIsGoneIsAssignedAgain(t *testing.T) {
	e := usenetEntry(commonv1.DownloadPhaseAssigned)
	e.Client, e.Engine, e.Dispatch.Destination = "usenet-local", "usenet-local-0", "usenet-local-0"
	e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-3 * time.Hour))
	v := view([]catalogv1alpha1.DownloadEntry{e})
	// The gone client's engine record lingers until the bucket's TTL.
	v.Engines["usenet-local-0"] = lifecycle.Engine{
		Name: "usenet-local-0", Client: "usenet-local", Ready: true, Reattached: true, BootID: "boot-0",
		At: now.Add(-30 * time.Hour), ReattachedAt: now.Add(-40 * time.Hour),
	}

	p := lifecycle.Decide(v)
	got := only(p)
	require.NotNil(t, got)
	assert.Equal(t, commonv1.DownloadPhaseAssigned, got.Phase)
	assert.Equal(t, "sab", got.Client)
	assert.Equal(t, "sab-0", got.Engine)
	var adds []string
	for _, c := range p.Commands {
		if c.Cmd.Desired == schema.EngineDesiredPresent {
			adds = append(adds, c.Engine)
		}
	}
	assert.Equal(t, []string{"sab-0"}, adds)
}

// A transfer under way keeps its client: only an entry whose transfer never
// started is moved.
func TestADownloadingEntryWhoseClientIsGoneIsNotMoved(t *testing.T) {
	e := usenetEntry(commonv1.DownloadPhaseDownloading)
	e.Client, e.Engine, e.Dispatch.Destination = "usenet-local", "usenet-local-0", "usenet-local-0"
	v := view([]catalogv1alpha1.DownloadEntry{e})
	got := only(lifecycle.Decide(v))
	require.NotNil(t, got)
	assert.Equal(t, "usenet-local", got.Client)
}
