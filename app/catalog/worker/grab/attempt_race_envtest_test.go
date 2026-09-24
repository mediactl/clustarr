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

package grab_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
)

// TestRecordSearchAttempt_AnInterleavedWriterIsNotRolledBack is the
// cross-replica race RecordSearchAttempt's read-modify-declare used to lose.
//
// Every replica runs the search, grab and RSS consumers. The search worker on
// one replica reads the Movie; before it declares the bumped attempt count,
// another replica writes the same manager's set -- and the declare, built
// from the read, carried the pre-write pendingGrab with ForceOwnership. A
// grab that had just consumed the pending candidate got it resurrected (the
// movie back at Phase=Delayed, over a running Download); a delayed decision
// that had just recorded one got it released (the scheduled grab still
// coming, the movie back in the wanted sweep).
//
// No release test can see this: every field IS declared, just with values
// from before the other write. The only test that can is one with a real
// second writer inside the window, which is what the interceptor provides --
// the other replica's real code path runs between RecordSearchAttempt's first
// read of the Movie and its write.
func TestRecordSearchAttempt_AnInterleavedWriterIsNotRolledBack(t *testing.T) {
	tests := []struct {
		name string
		// seedPending puts the movie at the delayed steady state first.
		seedPending bool
		// writer is the other replica's write, run inside the window.
		writer func(t *testing.T, ctx context.Context, c client.Client, ns string, movie *catalogv1alpha1.Movie)
		// wantPending is whether status.pendingGrab is set afterwards.
		wantPending bool
	}{
		{
			name:        "a grab consuming the pending candidate is not resurrected",
			seedPending: true,
			writer: func(t *testing.T, ctx context.Context, c client.Client, ns string, movie *catalogv1alpha1.Movie) {
				profile := hdBlurayWeb(t)
				target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
				bus := newTestBus(t, nil)
				release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
				seedPending(t, ctx, bus, ns, target, nil, release, downloadv1alpha1.GrabSourceRSS)
				h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)})
				require.NoError(t, h.Handle(ctx, grabTaskMessage(t, ns, target, nil)))
			},
			wantPending: false,
		},
		{
			name: "a delayed decision recording a pending candidate is not released",
			writer: func(t *testing.T, ctx context.Context, c client.Client, ns string, movie *catalogv1alpha1.Movie) {
				profile := hdBlurayWeb(t)
				require.NoError(t, grab.Decide(ctx,
					grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)},
					profile,
					catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 45, BypassIfHighestQuality: boolPtr(false)},
					grab.Approved{
						Namespace: ns,
						Target:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
						Release:   torrentRelease("guid-2", "my-indexer", profile.Tiers[len(profile.Tiers)-1][0].Quality, 0),
						GrabbedBy: downloadv1alpha1.GrabSourceRSS,
					}))
			},
			wantPending: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := newTestClient(t)
			ns := newNamespace(t, ctx, c)
			movie := newMovie(t, ctx, c, ns, "the-thing-1982")
			newIndexer(t, ctx, c, ns, "my-indexer", nil)
			seedGatewayMetadata(t, ctx, c, movie)
			if tc.seedPending {
				seedWorkerStatus(t, ctx, c, movie, "", &catalogv1alpha1.PendingGrab{
					ReleaseTitle: "The.Thing.1982.1080p.BluRay.x264-GROUP",
					Protocol:     commonv1.ProtocolTorrent,
					GrabAt:       metav1.NewTime(testNow.Add(45 * time.Minute)),
				})
			}

			interleaved := false
			racing := interceptor.NewClient(newWatchClient(t), interceptor.Funcs{
				Get: func(ctx context.Context, wc client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := wc.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					// The search worker now holds its read. The other
					// replica writes, on its own client, before the
					// search worker declares.
					if _, isMovie := obj.(*catalogv1alpha1.Movie); isMovie && !interleaved {
						interleaved = true
						tc.writer(t, ctx, c, ns, movie)
					}
					return nil
				},
			})

			require.NoError(t, grab.RecordSearchAttempt(ctx, racing, ns,
				commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}, testNow))
			require.True(t, interleaved, "the interleaved writer never ran; the test proves nothing")

			var got catalogv1alpha1.Movie
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
			if tc.wantPending {
				assert.NotNilf(t, got.Status.PendingGrab,
					"the search attempt released the pendingGrab another replica recorded inside its window")
			} else {
				assert.Nilf(t, got.Status.PendingGrab,
					"the search attempt resurrected the pendingGrab another replica's grab had consumed")
			}
			// And the attempt itself still landed, exactly once.
			require.NotNil(t, got.Status.LastSearchedAt)
			assert.True(t, got.Status.LastSearchedAt.Time.Equal(testNow))
			assert.EqualValues(t, 1, got.Status.SearchAttempts.Count)
			require.NotNil(t, got.Status.Metadata, "a different manager's field is never touched")
		})
	}
}
