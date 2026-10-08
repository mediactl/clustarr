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

package starttest

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/overlayplan"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// overlayContentType is every overlay's Content-Type (the renderer's
// ContentTypeJPEG, restated so this package does not link the renderer).
const overlayContentType = "image/jpeg"

// VerifyRenderer proves the overlay renderer (agent-catalog's
// catalog-artwork-render consumer) runs on the real bus: a Movie in the
// metadata stage's steady state (an original poster in clustarr-artwork,
// stored with its complete metadata through catalogartwork.ObjectMeta and
// recorded in status.artwork beside a rated status.metadata) and an
// OverlayProfile selecting it end with poster/overlay stored, rendered from
// that original under that profile. The renderer writes objects only
// (ADR-0019 §7.2): status.overlay is the manager's overlay stage's, from
// the overlay object's metadata.
//
// viaController leaves the render task to the manager's overlay stage, which
// must then be running too: the profile is created last, its plan is what
// publishes, and the stage's status.overlay is checked against the stored
// overlay. Otherwise the probe publishes the task itself (overlayplan.Task,
// as the stage would) and checks the object alone, since no manager runs to
// record it.
func VerifyRenderer(t testing.TB, cfg *rest.Config, natsURL, name string, viaController bool) {
	t.Helper()
	ctx := context.Background()
	c := Client(t, cfg)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect to %s: %v", natsURL, err)
	}
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	if err != nil {
		t.Fatalf("open jetstream: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	store := bus.ObjectStore(events.BucketArtwork)

	lbls := map[string]string{"overlay": name}
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: lbls},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "q", RootFolderRef: "r"},
	}
	if err := c.Create(ctx, movie); err != nil {
		t.Fatalf("create Movie: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), movie) })

	poster := image.NewNRGBA(image.Rect(0, 0, 100, 150))
	for i := range poster.Pix {
		poster.Pix[i] = 0x80
	}
	poster.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := png.Encode(&buf, poster); err != nil {
		t.Fatalf("encode the poster: %v", err)
	}
	stored := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	const sourceURL = "https://img.example/p.png"
	ref := catalogartwork.RefOf(movie, commonv1alpha1.MediaKindMovie)
	orig, err := store.Put(ctx,
		events.ArtworkKey(commonv1alpha1.MediaKindMovie, movie.UID, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOriginal),
		&buf, catalogartwork.ObjectMeta(ref, catalogv1alpha1.ImageTypePoster, events.ArtworkVariantOriginal, catalogartwork.Facts{
			ContentType: "image/png", Source: string(catalogv1alpha1.ArtworkSourceProvider), SourceURL: sourceURL,
			Width: 100, Height: 150, UpdatedAt: stored,
		}))
	if err != nil {
		t.Fatalf("store the original: %v", err)
	}
	if _, err := k8s.PatchStatus(ctx, c, catalogstatus.GatewayManager, catalogac.Movie(name, "default").WithStatus(catalogac.MovieStatus().
		WithMetadata(catalogac.MovieMetadata().WithTitle("Heat").
			WithRatings(catalogac.Rating().WithSource(catalogv1alpha1.RatingSourceTMDB).WithValueCentis(781))).
		WithArtwork(catalogstatus.ArtworkEntries([]catalogv1alpha1.ArtworkEntry{{
			Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceProvider,
			SourceURL: sourceURL, Digest: orig.Digest, SizeBytes: orig.Size,
			UpdatedAt: metav1.NewTime(stored),
		}})...))); err != nil {
		t.Fatalf("apply the metadata stage's status: %v", err)
	}

	profile := &catalogv1alpha1.OverlayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: catalogv1alpha1.OverlayProfileSpec{
			Selector: &metav1.LabelSelector{MatchLabels: lbls},
			Badges:   []catalogv1alpha1.OverlayBadge{{Source: catalogv1alpha1.RatingSourceTMDB}},
		},
	}
	if err := c.Create(ctx, profile); err != nil {
		t.Fatalf("create OverlayProfile: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), profile) })

	if !viaController {
		var got catalogv1alpha1.Movie
		if err := c.Get(ctx, client.ObjectKeyFromObject(movie), &got); err != nil {
			t.Fatalf("get Movie: %v", err)
		}
		it, err := overlayplan.ItemOf(&got)
		if err != nil {
			t.Fatal(err)
		}
		subject, env, err := overlayplan.Task(it, "start-probe", overlayplan.ReasonPlan, time.Now())
		if err != nil {
			t.Fatalf("build the render task: %v", err)
		}
		if _, err := bus.Publish(ctx, subject, env); err != nil {
			t.Fatalf("publish the render task: %v", err)
		}
	}

	overlayKey := events.ArtworkKey(commonv1alpha1.MediaKindMovie, movie.UID, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOverlay)
	var info events.ObjectInfo
	WaitForLong(t, "the renderer to store poster/overlay", func() bool {
		info, err = store.Info(ctx, overlayKey)
		return err == nil
	})
	if got := info.Headers[events.HeaderContentType]; got != overlayContentType {
		t.Errorf("overlay Content-Type = %q, want %q", got, overlayContentType)
	}
	if got := info.Metadata[events.ArtworkMetaKeyProfile]; got != name {
		t.Errorf("overlay profile = %q, want %q", got, name)
	}
	if got := info.Metadata[events.ArtworkMetaKeyOriginalDigest]; got != orig.Digest {
		t.Errorf("overlay drawn on %q, want the stored original %q", got, orig.Digest)
	}
	if info.Headers[catalogartwork.HeaderRenderedFrom] == "" {
		t.Errorf("overlay carries no %s", catalogartwork.HeaderRenderedFrom)
	}
	if !viaController {
		return
	}

	var got catalogv1alpha1.Movie
	WaitForLong(t, "the manager's overlay stage to record status.overlay", func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(movie), &got) == nil && got.Status.Overlay != nil &&
			got.Status.Overlay.Digest == info.Digest
	})
	if got.Status.Overlay.ProfileRef != name {
		t.Errorf("status.overlay.profileRef = %q, want %q", got.Status.Overlay.ProfileRef, name)
	}
	if got.Status.Overlay.RenderedFrom != info.Headers[catalogartwork.HeaderRenderedFrom] {
		t.Errorf("status.overlay %+v does not describe the stored overlay %+v", got.Status.Overlay, info)
	}
}
