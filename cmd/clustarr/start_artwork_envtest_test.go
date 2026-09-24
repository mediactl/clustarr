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

package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/overlayprofile"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/app/catalog/worker/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// verifyRenderer proves spec §C.6's renderer runs in the case's process on
// the real bus, under catalogarr's RBAC: a Movie in the gateway's steady
// state (an original poster in clustarr-artwork, recorded in
// status.artwork beside a rated status.metadata) and an OverlayProfile
// selecting it end with poster/overlay stored and status.overlay recorded
// -- the Get, the OverlayProfile list and the movies/status patch are all
// the renderer's, made as the catalogarr ServiceAccount.
//
// viaController leaves the render task to the OverlayProfile controller,
// which must then be running too (app/catalog/all): the profile is created
// last, and its first reconcile is what publishes. Otherwise the probe
// publishes the task itself, as the gateway would (app/catalog/artwork,
// whose role runs no controller).
func verifyRenderer(t *testing.T, cfg *rest.Config, natsURL, name string, viaController bool) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	bus, nc, err := k8s.ConnectBus(natsURL, "start-test")
	if err != nil {
		t.Fatalf("connect the bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close(); nc.Close() })
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
	orig, err := store.Put(ctx, events.ArtworkKey(commonv1alpha1.MediaKindMovie, movie.UID, "poster", events.ArtworkVariantOriginal),
		&buf, map[string]string{"Content-Type": "image/png", "Clustarr-Source": "provider", "Clustarr-Source-URL": "https://img.example/p.png"})
	if err != nil {
		t.Fatalf("store the original: %v", err)
	}
	if _, err := k8s.PatchStatus(ctx, c, catalogstatus.GatewayManager, catalogac.Movie(name, "default").WithStatus(catalogac.MovieStatus().
		WithMetadata(catalogac.MovieMetadata().WithTitle("Heat").
			WithRatings(catalogac.Rating().WithSource(catalogv1alpha1.RatingSourceTMDB).WithValueCentis(781))).
		WithArtwork(catalogstatus.ArtworkEntries([]catalogv1alpha1.ArtworkEntry{{
			Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceProvider,
			SourceURL: "https://img.example/p.png", Digest: orig.Digest, SizeBytes: orig.Size,
			UpdatedAt: metav1.NewTime(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)),
		}})...))); err != nil {
		t.Fatalf("apply the gateway's status: %v", err)
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

	if viaController {
		waitFor(t, "the OverlayProfile controller to hash and count its selection", func() bool {
			var got catalogv1alpha1.OverlayProfile
			return c.Get(ctx, client.ObjectKeyFromObject(profile), &got) == nil &&
				got.Status.Hash == overlayprofile.Hash(got.Spec) && got.Status.Selected == 1
		})
	} else {
		var got catalogv1alpha1.Movie
		if err := c.Get(ctx, client.ObjectKeyFromObject(movie), &got); err != nil {
			t.Fatalf("get Movie: %v", err)
		}
		it, err := artwork.ItemOf(&got)
		if err != nil {
			t.Fatal(err)
		}
		if err := artwork.Publish(ctx, bus, it, "start-probe", "original"); err != nil {
			t.Fatalf("publish the render task: %v", err)
		}
	}

	var got catalogv1alpha1.Movie
	waitFor(t, "the renderer to record status.overlay", func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(movie), &got) == nil && got.Status.Overlay != nil
	})
	if got.Status.Overlay.ProfileRef != name {
		t.Errorf("status.overlay.profileRef = %q, want %q", got.Status.Overlay.ProfileRef, name)
	}
	info, err := store.Info(ctx, events.ArtworkKey(commonv1alpha1.MediaKindMovie, movie.UID, "poster", events.ArtworkVariantOverlay))
	if err != nil {
		t.Fatalf("the overlay object: %v", err)
	}
	if info.Digest != got.Status.Overlay.Digest || info.Headers[artwork.HeaderRenderedFrom] != got.Status.Overlay.RenderedFrom {
		t.Errorf("status.overlay %+v does not describe the stored overlay %+v", got.Status.Overlay, info)
	}
	if info.Headers["Content-Type"] != artwork.ContentTypeJPEG {
		t.Errorf("overlay Content-Type = %q", info.Headers["Content-Type"])
	}
}
