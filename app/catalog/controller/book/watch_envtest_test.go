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

package book_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// rankedEbookProfile is an ebook QualityProfile ranking AZW3 over EPUB, with its
// cutoff at the named tier.
func rankedEbookProfile(name, cutoff string) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindBook,
			Cutoff:    cutoff,
			Tiers: []catalogv1alpha1.Tier{
				{Name: "AZW3", Qualities: []string{"AZW3"}},
				{Name: "EPUB", Qualities: []string{"EPUB"}},
			},
		},
	}
}

// TestBookControllerWakesOnInheritedProfileEdits runs the Book's item path
// as the remediation loop runs it (itemtest.Start) against a Book with no
// profile override of its own, so it is ranked against its Author's. Both
// ways that
// inherited profile changes must reach it without any other event: an edit
// to the QualityProfile itself (mapQualityProfile's Author hop), and the
// Author pointing at another profile (the Author watch's spec arm).
func TestBookControllerWakesOnInheritedProfileEdits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	r := &book.Reconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorder("book"), Bus: fakePublisher{}}
	require.NoError(t, remediation.RegisterIndexes(ctx, mgr.GetFieldIndexer()))
	require.NoError(t, itemtest.Start(mgr, map[remediation.KeyKind]rollup.Item{remediation.KindBook: r}))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	c := mgr.GetClient()

	const ns = "book-watch-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	createRootFolder(t, ctx, c, ns, "book-root", "/data/media/books")
	require.NoError(t, c.Create(ctx, rankedEbookProfile("book-watch-qp", "EPUB")))
	require.NoError(t, c.Create(ctx, rankedEbookProfile("book-watch-qp-strict", "AZW3")))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "le-guin", Namespace: ns},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: "OL31574A", QualityProfileRef: "book-watch-qp", RootFolderRef: "book-root",
		},
	}))
	authorRef := "le-guin"
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "earthsea", Namespace: ns},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL59863W", AuthorRef: &authorRef},
	}))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "earthsea-epub", Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "earthsea"},
			Path:     "/data/media/books/Ursula K. Le Guin/earthsea.epub",
			Quality:  commonv1.Quality{Name: "EPUB"},
		},
	}))
	key := types.NamespacedName{Namespace: ns, Name: "earthsea"}
	cutoffMet := func(want bool) func() bool {
		return func() bool {
			var got catalogv1alpha1.Book
			return c.Get(ctx, key, &got) == nil && got.Status.HasFile && got.Status.CutoffMet == want
		}
	}
	require.Eventually(t, cutoffMet(true), 10*time.Second, 20*time.Millisecond, "setup: an EPUB under an EPUB cutoff never met it")

	var qp catalogv1alpha1.QualityProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "book-watch-qp"}, &qp))
	qp.Spec.Cutoff = "AZW3"
	require.NoError(t, c.Update(ctx, &qp))
	require.Eventually(t, cutoffMet(false), 10*time.Second, 20*time.Millisecond,
		"raising the inherited profile's cutoff never reached the Book")

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "book-watch-qp"}, &qp))
	qp.Spec.Cutoff = "EPUB"
	require.NoError(t, c.Update(ctx, &qp))
	require.Eventually(t, cutoffMet(true), 10*time.Second, 20*time.Millisecond)

	var author catalogv1alpha1.Author
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "le-guin"}, &author))
	author.Spec.QualityProfileRef = "book-watch-qp-strict"
	require.NoError(t, c.Update(ctx, &author))
	require.Eventually(t, cutoffMet(false), 10*time.Second, 20*time.Millisecond,
		"the Author pointing at another profile never reached the Book")

	// DeadLettered: the DLQ projector's annotation on this object, already
	// in steady state, becomes the condition through the For() predicate's
	// annotation arm alone -- and folding it releases nothing else.
	var before catalogv1alpha1.Book
	require.NoError(t, c.Get(ctx, key, &before))
	annotated := before.DeepCopy()
	if annotated.Annotations == nil {
		annotated.Annotations = map[string]string{}
	}
	annotated.Annotations[k8s.AnnotationDeadLettered] = "clustarr.work.metadata.normal.x@2026-09-23T10:00:00Z"
	require.NoError(t, c.Patch(ctx, annotated, client.MergeFrom(&before)))
	var got catalogv1alpha1.Book
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionDeadLettered)
	}, 10*time.Second, 20*time.Millisecond, "the dead-lettered annotation never became the DeadLettered condition")
	assert.Equal(t, before.Status.CutoffMet, got.Status.CutoffMet)
	assert.Equal(t, before.Status.FileFormat, got.Status.FileFormat)
	assert.Equal(t, before.Status.Path, got.Status.Path)
	assert.Equal(t, before.Status.Phase, got.Status.Phase)
	assert.NotNil(t, k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady), "folding DeadLettered released Ready")

	cleared := got.DeepCopy()
	delete(cleared.Annotations, k8s.AnnotationDeadLettered)
	require.NoError(t, c.Patch(ctx, cleared, client.MergeFrom(&got)))
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && k8s.FindCondition(got.Status.Conditions, k8s.ConditionDeadLettered) == nil
	}, 10*time.Second, 20*time.Millisecond, "removing the annotation never cleared the condition")
	// Gap-fix ruling R-5, on this steady-state Book: status.activeDownloadRef
	// derives from the grab entries on the Book's own status.downloads
	// (ADR-0019 §6.11), and this reconciler is the field's only writer. No
	// item watch wakes on an entry write (the stages decide entries inside
	// the pass that applies them), so each leg seeds the entries, waits for
	// the cache to see them, and reconciles the Book itself.
	require.NoError(t, c.Get(ctx, key, &got))
	steady := got.DeepCopy()
	seed := func(phase commonv1.DownloadPhase) {
		t.Helper()
		require.NoError(t, itemtest.SeedEntries(ctx, c, &got, itemtest.Entry("earthsea-dl", phase)))
		require.Eventually(t, func() bool {
			var g catalogv1alpha1.Book
			return c.Get(ctx, key, &g) == nil && len(g.Status.Downloads) == 1 && g.Status.Downloads[0].Phase == phase
		}, 10*time.Second, 20*time.Millisecond, "the cache never saw the seeded entry")
		_, err := r.ReconcileItem(ctx, key)
		require.NoError(t, err)
	}

	seed(commonv1.DownloadPhasePending)
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.ActiveDownloadRef != nil &&
			*got.Status.ActiveDownloadRef == "earthsea-dl" && got.Status.Phase == catalogv1alpha1.BookPhaseDownloading
	}, 10*time.Second, 20*time.Millisecond, "a non-terminal entry must set the ref and read Downloading")
	assert.Equal(t, steady.Status.FileFormat, got.Status.FileFormat, "the ref's apply released status.fileFormat")
	assert.Equal(t, steady.Status.CutoffMet, got.Status.CutoffMet)
	var refOwners []string
	for _, e := range got.ManagedFields {
		if e.Subresource == "status" && e.FieldsV1 != nil && strings.Contains(e.FieldsV1.GetRawString(), `"f:activeDownloadRef"`) {
			refOwners = append(refOwners, e.Manager)
		}
	}
	assert.Equal(t, []string{string(k8s.ManagerCatalog)}, refOwners, "status.activeDownloadRef has one writer")

	// Completed is on disk, awaiting import: still the item's grab (R-12).
	seed(commonv1.DownloadPhaseCompleted)
	require.Never(t, func() bool {
		var g catalogv1alpha1.Book
		return c.Get(ctx, key, &g) == nil && (g.Status.ActiveDownloadRef == nil || g.Status.Phase != catalogv1alpha1.BookPhaseDownloading)
	}, 500*time.Millisecond, 20*time.Millisecond, "a Completed entry is still working on the Book")

	// Imported is terminal: the ref goes and the phase is the file's again.
	seed(commonv1.DownloadPhaseImported)
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.ActiveDownloadRef == nil && got.Status.Phase == steady.Status.Phase
	}, 10*time.Second, 20*time.Millisecond, "a terminal entry must clear the ref and give the phase back")
}
