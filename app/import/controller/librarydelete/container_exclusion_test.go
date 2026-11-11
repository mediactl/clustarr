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

package librarydelete

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/controller/importexclusion"
	"github.com/mediactl/clustarr/app/import/manager/scanapply"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/names"
)

// A deletable container the root scan creates from its folder (the
// 2026-11-10 non-video libraries design's D16): an Artist, by its
// MusicBrainz artist id, and an Author, by its Open Library author id.
type containerCase struct {
	kind     commonv1.MediaKind
	item     func() client.Object
	wantKind catalogv1alpha1.ExclusionKind
	wantIDs  map[string]string
	title    string
	// create is the scan's proposal of the same item from its folder, and
	// rel the observed path under the root of rootKind.
	create   *schema.ItemCreate
	rel      string
	rootKind catalogv1alpha1.RootFolderKind
}

func containerCases() []containerCase {
	const mbid, olid = "83d91898-7763-47d7-b03b-b92132375c47", "OL26320A"
	return []containerCase{{
		kind: commonv1.MediaKindArtist, wantKind: catalogv1alpha1.ExclusionKindArtist,
		wantIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyMusicBrainz: mbid}, title: "Pink Floyd",
		item: func() client.Object {
			return &catalogv1alpha1.Artist{
				ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: names.Artist("Pink Floyd", mbid)},
				Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: mbid, QualityProfileRef: "lossless", RootFolderRef: "music"},
				Status:     catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Pink Floyd"}},
			}
		},
		create: &schema.ItemCreate{
			Kind: string(commonv1.MediaKindArtist), MusicBrainzID: mbid, Title: "Pink Floyd", Folder: "Pink Floyd",
		},
		rel: "Pink Floyd/01 - Speak to Me.flac", rootKind: catalogv1alpha1.RootFolderKindMusic,
	}, {
		kind: commonv1.MediaKindAuthor, wantKind: catalogv1alpha1.ExclusionKindAuthor,
		wantIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyOpenLibrary: olid}, title: "J. R. R. Tolkien",
		item: func() client.Object {
			return &catalogv1alpha1.Author{
				ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: names.Author("J. R. R. Tolkien", olid)},
				Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: olid, QualityProfileRef: "ebook", RootFolderRef: "music"},
				Status:     catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: "J. R. R. Tolkien"}},
			}
		},
		create: &schema.ItemCreate{
			Kind: string(commonv1.MediaKindAuthor), OpenLibraryID: olid, Title: "J. R. R. Tolkien", Folder: "J.R.R. Tolkien",
		},
		rel: "J.R.R. Tolkien/The Hobbit.epub", rootKind: catalogv1alpha1.RootFolderKindBook,
	}}
}

// deleteWithExclusion runs the delete of tc's item, asked for as the delete
// dialog asks with its exclusion box ticked (records, add-exclusion), and
// returns the ImportExclusion it wrote.
func deleteWithExclusion(t *testing.T, tc containerCase) catalogv1alpha1.ImportExclusion {
	t.Helper()
	item := tc.item()
	item.SetAnnotations(map[string]string{
		catalogv1alpha1.AnnotationDelete:             catalogv1alpha1.DeleteRecords,
		catalogv1alpha1.AnnotationDeleteAddExclusion: "true",
	})
	c := newClient(t, musicRoot(t.TempDir()), item)
	require.NoError(t, reconcileKind(t, c, tc.kind, item.GetName()))
	assert.True(t, gone(t, c, key(item.GetName()), tc.item()), "the %s was not deleted", tc.kind)
	var ex catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(context.Background(), key(item.GetName()), &ex), "the delete wrote no exclusion")
	return ex
}

// An Artist's and an Author's delete writes the ImportExclusion the delete
// dialog's box asks for, as a Movie's does (the 2026-11-10 non-video
// design's D16; plan W7.3): named after the item, of kind artist under the
// musicbrainz key or author under the openlibrary key, titled by the
// metadata's name -- the key the scan applier looks up before it creates
// the container a root scan finds by its folder.
func TestAnArtistOrAuthorDeleteWritesItsExclusion(t *testing.T) {
	for _, tc := range containerCases() {
		t.Run(string(tc.kind), func(t *testing.T) {
			ex := deleteWithExclusion(t, tc)
			assert.Equal(t, tc.wantKind, ex.Spec.Kind)
			assert.Equal(t, tc.wantIDs, ex.Spec.ExternalIDs)
			assert.Equal(t, tc.title, ex.Spec.Title)
			assert.Equal(t, exclusionReason, ex.Spec.Reason)
		})
	}
}

// The box holds (catalogv1alpha1.ExclusionKindFor's promise for an Artist
// and an Author): the exclusion an Artist's or an Author's delete writes is
// one the apiserver takes, the importexclusion controller indexes it, and
// the scan applier then refuses the root scan's proposal of the same item
// from its folder -- listed unmatched, excluded, naming the exclusion --
// so the deleted container is not created again.
func TestADeletedArtistOrAuthorIsNotCreatedAgainByTheRootScan(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	for _, tc := range containerCases() {
		t.Run(string(tc.kind), func(t *testing.T) {
			ns := "librarydelete-rescan-" + string(tc.kind)
			require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
			bus := membus.New(clockwork.NewRealClock())
			require.NoError(t, bus.Ensure(ctx, events.Default()))
			t.Cleanup(func() { _ = bus.Close() })

			written := deleteWithExclusion(t, tc)
			ex := &catalogv1alpha1.ImportExclusion{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: written.Name},
				Spec:       written.Spec,
			}
			require.NoError(t, c.Create(ctx, ex), "the written exclusion is one the CRD accepts")
			_, err := (&importexclusion.Reconciler{Client: c, Bus: bus}).Reconcile(ctx,
				ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ex)})
			require.NoError(t, err)

			root := &catalogv1alpha1.RootFolder{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "library"},
				Spec: catalogv1alpha1.RootFolderSpec{
					Path: "/data/media/library", Kind: tc.rootKind,
					Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "default"},
				},
			}
			require.NoError(t, c.Create(ctx, root))
			now := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
			create := *tc.create
			create.RootFolderRef = root.Name
			scan := schema.Ref{Namespace: ns, Name: "scan", UID: ns + "-scan"}
			a := &scanapply.Applier{Client: c, Reader: c, APIReader: c, Bus: bus, Now: func() time.Time { return now }}
			require.NoError(t, a.Apply(ctx, schema.ScanObservation{
				Scan: scan, RootFolder: schema.Ref{Namespace: ns, Name: root.Name},
				Kind: schema.ScanObservationPresent, Path: root.Spec.Path + "/" + tc.rel, SizeBytes: 1, ModTime: now,
				Attribution: &schema.ScanAttribution{Kind: create.Kind, Evidence: "folder", Create: &create}, SeenAt: now,
			}))

			obj := tc.item()
			assert.True(t, gone(t, c, client.ObjectKey{Namespace: ns, Name: obj.GetName()}, obj),
				"the root scan created the deleted %s again", tc.kind)
			entry, err := bus.KV(events.BucketProgress).Get(ctx, scanprogress.AppliedKey(scan.UID))
			require.NoError(t, err, "the applier recorded no tally")
			p, err := scanprogress.DecodeProgress(entry.Value)
			require.NoError(t, err)
			require.Len(t, p.Unmatched, 1)
			assert.Contains(t, p.Unmatched[0].Reason, "excluded")
			assert.Contains(t, p.Unmatched[0].Reason, ns+"/"+written.Name)
		})
	}
}
