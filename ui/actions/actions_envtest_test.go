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

package actions_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/ui/actions"
)

// creatorManager stands in for whichever controller or person created a
// catalog item -- the Series fan-out's r.Create, importarr, kubectl. It is a
// create, so an Update-operation entry, exactly like the fan-out's.
const creatorManager = "test-creator"

// monitoredOnly is the whole of what clustarr-ui may own on a catalog item
// after "monitor this": spec.monitored, one leaf, nothing else.
const monitoredOnly = `{"f:spec":{"f:monitored":{}}}`

// itemFixtures builds one valid object of every kind SetMonitored accepts,
// created with spec.monitored=true the way the Series fan-out sets it at
// creation. Required fields and patterns are from config/crd/bases.
var itemFixtures = map[commonv1.MediaKind]func(name, ns string) client.Object{
	commonv1.MediaKindMovie: func(name, ns string) client.Object {
		return &catalogv1alpha1.Movie{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: "hd-1080p", RootFolderRef: "movies", Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindSeries: func(name, ns string) client.Object {
		return &catalogv1alpha1.Series{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 81189, QualityProfileRef: "hd-1080p", RootFolderRef: "tv", Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindEpisode: func(name, ns string) client.Object {
		return &catalogv1alpha1.Episode{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.EpisodeSpec{
			SeriesRef: "breaking-bad", SeasonNumber: 1, EpisodeNumber: 1, Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindArtist: func(name, ns string) client.Object {
		return &catalogv1alpha1.Artist{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "lossless",
			RootFolderRef: "music", Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindAlbum: func(name, ns string) client.Object {
		return &catalogv1alpha1.Album{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.AlbumSpec{
			ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e666-3926-a536-22c65f834433", Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindAuthor: func(name, ns string) client.Object {
		return &catalogv1alpha1.Author{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: "OL23919A", QualityProfileRef: "ebook", RootFolderRef: "books", Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindBook: func(name, ns string) client.Object {
		return &catalogv1alpha1.Book{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.BookSpec{
			WorkID: "OL82563W", Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindAudiobook: func(name, ns string) client.Object {
		return &catalogv1alpha1.Audiobook{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.AudiobookSpec{
			ASIN: "B017V4IM1G", QualityProfileRef: "audiobook", RootFolderRef: "audiobooks", Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindComic: func(name, ns string) client.Object {
		return &catalogv1alpha1.Comic{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-2127", QualityProfileRef: "comic",
			RootFolderRef: "comics", Monitored: ptr.To(true),
		}}
	},
	commonv1.MediaKindIssue: func(name, ns string) client.Object {
		return &catalogv1alpha1.Issue{ObjectMeta: meta(name, ns), Spec: catalogv1alpha1.IssueSpec{
			ComicRef: "saga", Number: "1", CalculatedNumberCentis: 100, Monitored: ptr.To(true),
		}}
	},
}

func meta(name, ns string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: ns} }

// TestUIManagerNeverOwnsStatus is ruling R2's runtime guard, and the
// machine-checkable form of D3-5's e2e assertion that no UI manager appears
// on any status path: it performs every ui/actions action against a real
// apiserver, each on an object that ALREADY HAS STATUS written by the
// controller that owns it (a blank object has nothing to lose, so could not
// show a status release or takeover), and then reads metadata.managedFields.
//
// ui/guard_test.go proves the source makes no status call; this proves what
// the apiserver recorded. Specifically:
//
//   - clustarr-ui never has a managedFields entry on the status subresource,
//     and no clustarr-ui entry anywhere mentions f:status;
//   - after "monitor this" it owns exactly f:spec.f:monitored -- asserted on
//     managedFields, because an over-claim is silent on the object's values
//     (CLAUDE.md) -- and has taken that leaf from the create-time manager,
//     which is the audit trail ruling R2 wants;
//   - the owning controller's status, and its status entry, are untouched;
//   - "monitor this" on a missing item is NotFound and creates nothing;
//   - the (group, resource, verb) each action actually hit is exactly
//     actions.Grants(), which cmd/clustarr/ui_rbac_test.go holds
//     config/rbac/ui_role.yaml to. envtest does not enforce RBAC, so this is
//     where a kind the role forgot would show up.
func TestUIManagerNeverOwnsStatus(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	ctx := t.Context()
	const ns = "default"
	rec := &recordingWriter{c: c}

	t.Run("monitor this", func(t *testing.T) {
		for _, kind := range actions.MediaKinds() {
			t.Run(string(kind), func(t *testing.T) {
				fixture, ok := itemFixtures[kind]
				require.True(t, ok, "no fixture for %s; add one", kind)
				obj := fixture("item-"+string(kind), ns)
				require.NoError(t, c.Create(ctx, obj, client.FieldOwner(creatorManager)))
				gvk := mustGVK(t, obj, scheme)
				seedStatus(ctx, t, c, k8s.ManagerCatalogarr, gvk, obj.GetName(), ns)
				before := getUnstructured(ctx, t, c, gvk, obj.GetName(), ns)
				require.NotNil(t, before.Object["status"], "the fixture must already have status")

				for _, monitored := range []bool{false, true} {
					_, err := actions.SetMonitored(ctx, rec, ns, kind, obj.GetName(), monitored)
					require.NoError(t, err)

					after := getUnstructured(ctx, t, c, gvk, obj.GetName(), ns)
					requireNeverOnStatus(t, after)
					requireUIOwnsExactly(t, after, monitoredOnly)
					requireStatusStillOwnedBy(t, after, k8s.ManagerCatalogarr.String())
					requireManagerDoesNotOwn(t, after, creatorManager, "f:spec", "f:monitored")

					got, found, err := unstructured.NestedBool(after.Object, "spec", "monitored")
					require.NoError(t, err)
					require.True(t, found)
					require.Equal(t, monitored, got)
					require.Equal(t, before.Object["status"], after.Object["status"],
						"a spec patch must leave the controller's status exactly as it was")
				}
			})
		}
	})

	t.Run("monitor this on a missing item is NotFound and creates nothing", func(t *testing.T) {
		_, err := actions.SetMonitored(ctx, rec, ns, commonv1.MediaKindMovie, "never-existed", false)
		require.True(t, apierrors.IsNotFound(err), "want NotFound, got %v", err)
		getErr := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "never-existed"}, &catalogv1alpha1.Movie{})
		require.True(t, apierrors.IsNotFound(getErr), "the patch must not have created the Movie: %v", getErr)
	})

	t.Run("search now", func(t *testing.T) {
		s, err := actions.SearchNow(ctx, rec, ns, commonv1.MediaKindMovie, "item-movie")
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(s.Name, "item-movie-"), "generated name %q", s.Name)
		require.Equal(t, actions.OriginUI, s.Labels[actions.LabelOrigin])

		gvk := mustGVK(t, s, scheme)
		seedStatus(ctx, t, c, k8s.ManagerCatalogarr, gvk, s.Name, ns)
		got := getUnstructured(ctx, t, c, gvk, s.Name, ns)

		entry := requireOneUIEntry(t, got)
		requireFieldsContain(t, entry, "f:metadata", "f:labels", "f:"+actions.LabelOrigin)
		requireFieldsContain(t, entry, "f:spec", "f:mediaRef", "f:name")
		requireNeverOnStatus(t, got)
		requireStatusStillOwnedBy(t, got, k8s.ManagerCatalogarr.String())
	})

	t.Run("rescan", func(t *testing.T) {
		scan, err := actions.Rescan(ctx, rec, ns, "movies")
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(scan.Name, "movies-"), "generated name %q", scan.Name)
		require.Equal(t, actions.OriginUI, scan.Labels[actions.LabelOrigin])

		gvk := mustGVK(t, scan, scheme)
		seedStatus(ctx, t, c, k8s.ManagerImportarr, gvk, scan.Name, ns)
		got := getUnstructured(ctx, t, c, gvk, scan.Name, ns)

		entry := requireOneUIEntry(t, got)
		requireFieldsContain(t, entry, "f:metadata", "f:labels", "f:"+actions.LabelOrigin)
		requireFieldsContain(t, entry, "f:spec", "f:rootFolderRef")
		requireNeverOnStatus(t, got)
		requireStatusStillOwnedBy(t, got, k8s.ManagerImportarr.String())
	})

	// A sweep over every object of every kind the UI can touch, after all
	// of the above: whatever path a write took, clustarr-ui is on no status.
	// It counts what it inspected, so it cannot pass by looking at nothing.
	t.Run("no clustarr-ui entry on any status path, anywhere", func(t *testing.T) {
		inspected := 0
		for _, g := range actions.Grants() {
			gvk, err := c.RESTMapper().KindFor(schema.GroupVersionResource{Group: g.Group, Resource: g.Resource})
			require.NoError(t, err, "the apiserver serves no %s in %s", g.Resource, g.Group)
			list := &metav1.PartialObjectMetadataList{}
			list.SetGroupVersionKind(gvk)
			require.NoError(t, c.List(ctx, list, client.InNamespace(ns)))
			for i := range list.Items {
				for _, e := range list.Items[i].ManagedFields {
					if e.Manager == actions.FieldManager {
						inspected++
					}
				}
				requireNeverOnStatus(t, &list.Items[i])
			}
		}
		require.Equal(t, len(actions.MediaKinds())+2, inspected,
			"expected one clustarr-ui entry per catalog item patched plus the Search and the LibraryScan")
	})

	// This subtest asserted declared == used until Task G3-4 added
	// settings.go's eight Settings-page actions: actions.Grants() now
	// declares their grants too, but this envtest -- written for the three
	// §A3.2 actions (SearchNow, Rescan, SetMonitored) -- never calls them,
	// so "used" is a proper subset of "declared" by design, not by a bug.
	// The invariant this subtest actually protects -- no action ever hits an
	// undeclared (group, resource, verb), which would pass every test here
	// and be Forbidden only in a real cluster -- still holds as a subset
	// check; the settings actions get the same real-apiserver proof from
	// settings_envtest_test.go (one kind, chosen for its non-pointer,
	// omitempty-tagged Enabled field) plus settings_test.go's fakeWriter
	// coverage of the rest, and cmd/clustarr/ui_rbac_test.go still holds
	// config/rbac/ui_role.yaml to the whole of actions.Grants().
	t.Run("the grants the actions used are declared in actions.Grants()", func(t *testing.T) {
		used := map[actions.Grant]bool{}
		for _, call := range rec.calls() {
			gvk, err := apiutil.GVKForObject(call.obj, scheme)
			require.NoError(t, err)
			mapping, err := c.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
			require.NoError(t, err)
			used[actions.Grant{Group: mapping.Resource.Group, Resource: mapping.Resource.Resource, Verb: call.verb}] = true
		}
		declared := map[actions.Grant]bool{}
		for _, g := range actions.Grants() {
			declared[g] = true
		}
		for g := range used {
			require.True(t, declared[g],
				"the actions hit %+v on a real apiserver, but actions.Grants() does not declare it -- "+
					"config/rbac/ui_role.yaml would never grant it, so this would pass every test here and "+
					"be Forbidden only in a real cluster", g)
		}
		require.Len(t, used, len(actions.MediaKinds())+2,
			"expected exactly one grant per §A3.2 action this envtest exercises -- create Search, create "+
				"LibraryScan, patch each MediaKind -- see this subtest's own comment for why actions.Grants() "+
				"itself is now larger than that")
	})
}

// recordingWriter is an actions.Writer over a real client that records the
// object and verb of every call, so the test can map them to the RBAC
// resource the apiserver routed them to.
type recordingWriter struct {
	c   client.Client
	mu  sync.Mutex
	log []writeCall
}

type writeCall struct {
	obj  client.Object
	verb string
}

func (r *recordingWriter) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	r.record(obj, "create")
	return r.c.Create(ctx, obj, opts...)
}

func (r *recordingWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	r.record(obj, "patch")
	return r.c.Patch(ctx, obj, patch, opts...)
}

func (r *recordingWriter) record(obj client.Object, verb string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, writeCall{obj: obj, verb: verb})
}

func (r *recordingWriter) calls() []writeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]writeCall(nil), r.log...)
}

func mustGVK(t *testing.T, obj runtime.Object, scheme *runtime.Scheme) schema.GroupVersionKind {
	t.Helper()
	gvk, err := apiutil.GVKForObject(obj, scheme)
	require.NoError(t, err)
	return gvk
}

func getUnstructured(
	ctx context.Context, t *testing.T, c client.Client, gvk schema.GroupVersionKind, name, ns string,
) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, u))
	return u
}

// seedStatus writes a Ready condition to the object's status through
// pkg/k8s.PatchStatus as fm -- the owning controller's real write path --
// so every action below acts on an object in its steady state.
func seedStatus(
	ctx context.Context, t *testing.T, c client.Client, fm k8s.FieldManager,
	gvk schema.GroupVersionKind, name, ns string,
) {
	t.Helper()
	ac := &statusSeed{
		apiVersion: gvk.GroupVersion().String(), kind: gvk.Kind, name: name, namespace: ns,
		status: map[string]any{"conditions": []any{map[string]any{
			"type": "Ready", "status": "True", "reason": "Seeded", "message": "seeded by the test",
			"lastTransitionTime": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
		}}},
	}
	_, err := k8s.PatchStatus(ctx, c, fm, ac)
	require.NoError(t, err)
}

// statusSeed is a minimal apply configuration of apiVersion, kind, name,
// namespace and status, so one helper can seed status on all twelve kinds
// (Search has no generated apply configuration at all).
type statusSeed struct {
	apiVersion, kind, name, namespace string
	status                            map[string]any
}

func (s *statusSeed) IsApplyConfiguration() {}
func (s *statusSeed) GetName() *string      { return &s.name }
func (s *statusSeed) GetNamespace() *string { return &s.namespace }
func (s *statusSeed) GetKind() *string      { return &s.kind }
func (s *statusSeed) GetAPIVersion() *string {
	return &s.apiVersion
}

func (s *statusSeed) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"apiVersion": s.apiVersion,
		"kind":       s.kind,
		"metadata":   map[string]any{"name": s.name, "namespace": s.namespace},
		"status":     s.status,
	})
}

// requireNeverOnStatus is the guard itself: no clustarr-ui managedFields
// entry is on the status subresource, and none mentions f:status.
func requireNeverOnStatus(t *testing.T, obj metav1.Object) {
	t.Helper()
	for _, e := range obj.GetManagedFields() {
		if e.Manager != actions.FieldManager {
			continue
		}
		require.NotEqual(t, "status", e.Subresource,
			"%s/%s: field manager %s has a managedFields entry on the status subresource -- the UI never "+
				"writes status (amendment §A3.2, ruling R2)", obj.GetNamespace(), obj.GetName(), e.Manager)
		require.NotContains(t, fieldsOf(t, e), "f:status",
			"%s/%s: field manager %s owns a status field", obj.GetNamespace(), obj.GetName(), e.Manager)
	}
}

// requireOneUIEntry returns obj's single clustarr-ui entry, which must be on
// the main resource and be an Update (a create or a merge patch, never an
// apply).
func requireOneUIEntry(t *testing.T, obj metav1.Object) metav1.ManagedFieldsEntry {
	t.Helper()
	var found []metav1.ManagedFieldsEntry
	for _, e := range obj.GetManagedFields() {
		if e.Manager == actions.FieldManager {
			found = append(found, e)
		}
	}
	require.Len(t, found, 1, "%s/%s: want exactly one %s entry, got %+v",
		obj.GetNamespace(), obj.GetName(), actions.FieldManager, found)
	require.Empty(t, found[0].Subresource)
	require.Equal(t, metav1.ManagedFieldsOperationUpdate, found[0].Operation)
	return found[0]
}

// requireUIOwnsExactly asserts the clustarr-ui entry's field set is exactly
// want -- no over-claim, which no value assertion can see.
func requireUIOwnsExactly(t *testing.T, obj metav1.Object, want string) {
	t.Helper()
	entry := requireOneUIEntry(t, obj)
	require.JSONEq(t, want, entry.FieldsV1.GetRawString(),
		"%s/%s: %s must own exactly this and nothing else", obj.GetNamespace(), obj.GetName(), actions.FieldManager)
}

func requireStatusStillOwnedBy(t *testing.T, obj metav1.Object, manager string) {
	t.Helper()
	for _, e := range obj.GetManagedFields() {
		if e.Manager == manager && e.Subresource == "status" {
			require.Contains(t, fieldsOf(t, e), "f:status")
			return
		}
	}
	require.Failf(t, "status ownership lost", "%s/%s: %s no longer has its status entry: %+v",
		obj.GetNamespace(), obj.GetName(), manager, obj.GetManagedFields())
}

func requireManagerDoesNotOwn(t *testing.T, obj metav1.Object, manager string, path ...string) {
	t.Helper()
	for _, e := range obj.GetManagedFields() {
		if e.Manager != manager {
			continue
		}
		_, found := lookup(fieldsOf(t, e), path...)
		require.False(t, found, "%s/%s: %s still owns %v after the UI set it",
			obj.GetNamespace(), obj.GetName(), manager, path)
	}
}

func requireFieldsContain(t *testing.T, e metav1.ManagedFieldsEntry, path ...string) {
	t.Helper()
	_, found := lookup(fieldsOf(t, e), path...)
	require.True(t, found, "%s does not own %v: %s", e.Manager, path, e.FieldsV1.GetRawString())
}

func fieldsOf(t *testing.T, e metav1.ManagedFieldsEntry) map[string]any {
	t.Helper()
	if e.FieldsV1 == nil {
		return map[string]any{}
	}
	var m map[string]any
	require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &m))
	return m
}

func lookup(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}
