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

package remediation_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/itemstatus"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// stubMovie is a Movie's own pass reduced to its catalog apply: the stored
// status re-sent with the pass's download contribution folded in, through
// itemstatus.Apply, as every item kind's renderer does (itempass). The
// staged pass around it is the loop's.
type stubMovie struct {
	r client.Reader
	c client.Client
}

func (s *stubMovie) ReconcileItem(ctx context.Context, nn types.NamespacedName) (reconcile.Result, error) {
	var m catalogv1alpha1.Movie
	if err := s.r.Get(ctx, nn, &m); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	p := itempass.From(ctx)
	st := catalogac.MovieStatus().WithObservedGeneration(m.Generation)
	if es := itempass.Downloads(p, m.Status.Downloads); len(es) > 0 {
		st = st.WithDownloads(itempass.EntryACs(es)...)
	}
	if ph := itempass.Phase(p, m.Status.DownloadPhase); ph != "" {
		st = st.WithDownloadPhase(ph)
	}
	conflicted, err := itemstatus.Apply(ctx, s.c, &m, catalogac.Movie(m.Name, m.Namespace).WithStatus(st))
	return itemstatus.Requeue(conflicted), err
}

func (s *stubMovie) Watches() []rollup.Watch { return nil }

// stubStage is an item stage whose plan a test sets; declares, when set, is
// the managers whose sets it contributes to (remediation.SetDeclarer).
type stubStage struct {
	name     remediation.ItemStageName
	plan     func(v *remediation.ItemView) (remediation.ItemResult, error)
	declares []k8s.FieldManager
}

func (s *stubStage) Name() remediation.ItemStageName { return s.name }
func (s *stubStage) Applies(remediation.KeyKind) bool { return true }
func (s *stubStage) Plan(_ context.Context, _ *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	return s.plan(v)
}

type declaringStage struct{ *stubStage }

func (s declaringStage) DeclaresSets(remediation.KeyKind) []k8s.FieldManager { return s.declares }

// itemHarness is one Movie, "heat", passed through a staged item path over
// stubMovie and the stages a test sets.
type itemHarness struct {
	e  *remediationtest.Env
	ir *remediation.ItemReconciler
	nn types.NamespacedName
}

func newItemHarness(t *testing.T, ns string, stages ...remediation.ItemStage) *itemHarness {
	t.Helper()
	e := remediationtest.Start(t)
	remediationtest.MustNamespace(t, t.Context(), e.Admin, ns)
	remediationtest.MustMovie(t, t.Context(), e.Admin, ns, "heat", "hd")
	ir := &remediation.ItemReconciler{
		Items:  map[remediation.KeyKind]rollup.Item{remediation.KindMovie: &stubMovie{r: e.Loop.Reader, c: e.Client()}},
		Stages: stages, Env: e.Loop, Client: e.Client(),
	}
	return &itemHarness{e: e, ir: ir, nn: types.NamespacedName{Namespace: ns, Name: "heat"}}
}

func (h *itemHarness) pass(t *testing.T) reconcile.Result {
	t.Helper()
	res, err := h.ir.Reconcile(t.Context(), remediation.Key{Kind: remediation.KindMovie, Namespace: h.nn.Namespace, Name: h.nn.Name})
	require.NoError(t, err)
	return res
}

func (h *itemHarness) movie(t *testing.T) *catalogv1alpha1.Movie {
	t.Helper()
	var m catalogv1alpha1.Movie
	require.NoError(t, h.e.Admin.Get(t.Context(), h.nn, &m))
	return &m
}

// movieApplies are the status applies the loop sent to the Movie since
// from: each one's precondition resourceVersion and its status keys.
func (h *itemHarness) movieApplies(from int) []map[string]any {
	var out []map[string]any
	for _, a := range h.e.StatusApplies()[from:] {
		if a["kind"] == "Movie" {
			out = append(out, a)
		}
	}
	return out
}

func rvOf(t *testing.T, a map[string]any) int {
	t.Helper()
	md, _ := a["metadata"].(map[string]any)
	s, _ := md["resourceVersion"].(string)
	n, err := strconv.Atoi(s)
	require.NoError(t, err, "an apply carried no resourceVersion precondition")
	return n
}

func statusKeys(a map[string]any) []string {
	st, _ := a["status"].(map[string]any)
	var out []string
	for k := range st {
		out = append(out, k)
	}
	return out
}

// owns reports whether manager owns status.<field> on obj.
func owns(entries []metav1.ManagedFieldsEntry, manager, field string) bool {
	st, _ := remediationtest.ManagedFieldPaths(entries, manager, "status")["f:status"].(map[string]any)
	_, ok := st["f:"+field]
	return ok
}

func grabbing(title string) remediation.ItemResult {
	return remediation.ItemResult{
		Contribution: itempass.Contribution{Downloads: &[]catalogv1alpha1.DownloadEntry{itemtest.Entry("e1", commonv1.DownloadPhaseQueued)}},
		Sets: []itempass.Set{{Manager: k8s.ManagerCatalogGrab, Fields: map[string]any{
			"pendingGrab": catalogv1alpha1.PendingGrab{ReleaseTitle: title, GrabAt: metav1.NewTime(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))},
		}}},
	}
}

func describing(title string) remediation.ItemResult {
	return remediation.ItemResult{Sets: []itempass.Set{{Manager: k8s.ManagerCatalogMetadata, Fields: map[string]any{
		"metadata": catalogv1alpha1.MovieMetadata{Title: title, RefreshedAt: metav1.NewTime(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))},
	}}}}
}

// TestOneApplyPerManagerCASChained (ADR-0019 §7.0): a pass with a grab set
// and a metadata set makes three applies -- the kind's catalog apply, then
// catalog-grab's, then catalog-metadata's -- each preconditioned on the
// resourceVersion the one before returned; each manager owns its own
// fields. A write by another manager between them makes the next set's
// apply a Conflict: the pass stops there, requeues in a second at watch
// priority, rolls nothing back, and the next pass completes it.
func TestOneApplyPerManagerCASChained(t *testing.T) {
	var grabTitle, metaTitle atomic.Value
	grabTitle.Store("Heat.1995.1080p.BluRay")
	metaTitle.Store("Heat")
	grab := &stubStage{name: remediation.StageGrab, plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
		return grabbing(grabTitle.Load().(string)), nil
	}}
	meta := &stubStage{name: remediation.StageMetadata, plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
		return describing(metaTitle.Load().(string)), nil
	}}
	h := newItemHarness(t, "chain", meta, grab) // given out of order: StageOrder runs grab first
	r0 := rvOf(t, map[string]any{"metadata": map[string]any{"resourceVersion": h.movie(t).ResourceVersion}})
	from := len(h.e.StatusApplies())
	res := h.pass(t)
	assert.Zero(t, res.RequeueAfter)
	applies := h.movieApplies(from)
	require.Len(t, applies, 3, "one apply per manager")
	assert.Equal(t, r0, rvOf(t, applies[0]), "the catalog apply is preconditioned on the view")
	assert.ElementsMatch(t, []string{"observedGeneration", "downloads"}, statusKeys(applies[0]), "the kind's own apply carries the stage's downloads")
	assert.ElementsMatch(t, []string{"pendingGrab"}, statusKeys(applies[1]), "catalog-grab's set second")
	assert.ElementsMatch(t, []string{"metadata"}, statusKeys(applies[2]), "catalog-metadata's set third")
	assert.Greater(t, rvOf(t, applies[1]), rvOf(t, applies[0]), "chained on the catalog apply's resourceVersion")
	assert.Greater(t, rvOf(t, applies[2]), rvOf(t, applies[1]), "chained on the grab set's resourceVersion")
	m := h.movie(t)
	final, err := strconv.Atoi(m.ResourceVersion)
	require.NoError(t, err)
	assert.Greater(t, final, rvOf(t, applies[2]))
	require.NotNil(t, m.Status.PendingGrab)
	assert.Equal(t, "Heat.1995.1080p.BluRay", m.Status.PendingGrab.ReleaseTitle)
	require.NotNil(t, m.Status.Metadata)
	assert.Equal(t, "Heat", m.Status.Metadata.Title)
	require.Len(t, m.Status.Downloads, 1)
	assert.True(t, owns(m.ManagedFields, "catalog", "downloads"))
	assert.True(t, owns(m.ManagedFields, "catalog-grab", "pendingGrab"))
	assert.True(t, owns(m.ManagedFields, "catalog-metadata", "metadata"))
	assert.False(t, owns(m.ManagedFields, "catalog", "pendingGrab"), "catalog over-claimed the grab set")
	assert.False(t, owns(m.ManagedFields, "catalog", "metadata"), "catalog over-claimed the metadata set")

	// Another manager writes between the catalog apply and the grab set.
	grabTitle.Store("Heat.1995.2160p.UHD.BluRay")
	metaTitle.Store("Heat (1995)")
	var calls atomic.Int32
	h.e.OnStatusPatch = func(client.Object) {
		if calls.Add(1) == 2 {
			mv := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "chain", Name: "heat"}}
			patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"example.com/note":"kept"}}}`))
			if err := h.e.Admin.Patch(context.Background(), mv, patch); err != nil {
				t.Errorf("interleave: %v", err)
			}
		}
	}
	from = len(h.e.StatusApplies())
	res = h.pass(t)
	h.e.OnStatusPatch = nil
	assert.Equal(t, time.Second, res.RequeueAfter, "a conflicted set requeues in a second")
	require.NotNil(t, res.Priority)
	assert.Equal(t, 0, *res.Priority)
	assert.Len(t, h.movieApplies(from), 2, "the pass stopped at the conflicted set")
	m = h.movie(t)
	assert.Equal(t, "Heat.1995.1080p.BluRay", m.Status.PendingGrab.ReleaseTitle, "the conflicted set landed")
	assert.Equal(t, "Heat", m.Status.Metadata.Title, "a set after the conflict landed")
	assert.Equal(t, "kept", m.Annotations["example.com/note"], "the interleaved writer was rolled back")

	h.pass(t)
	m = h.movie(t)
	assert.Equal(t, "Heat.1995.2160p.UHD.BluRay", m.Status.PendingGrab.ReleaseTitle, "the next pass completes the grab set")
	assert.Equal(t, "Heat (1995)", m.Status.Metadata.Title, "and the metadata set")
	assert.Equal(t, "kept", m.Annotations["example.com/note"], "the next pass rolled the other writer back")
}

// TestASetEqualAtItsPathsIsSkipped (ADR-0019 §7.0): a set equal to the
// stored values at its own paths is not applied; a set that changed is,
// alone; the kind's own catalog apply is never skipped.
func TestASetEqualAtItsPathsIsSkipped(t *testing.T) {
	var metaTitle atomic.Value
	metaTitle.Store("Heat")
	grab := &stubStage{name: remediation.StageGrab, plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
		return grabbing("Heat.1995.1080p.BluRay"), nil
	}}
	meta := &stubStage{name: remediation.StageMetadata, plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
		return describing(metaTitle.Load().(string)), nil
	}}
	h := newItemHarness(t, "setequal", grab, meta)
	h.pass(t)
	require.NotNil(t, h.movie(t).Status.Metadata, "setup")

	from := len(h.e.StatusApplies())
	h.pass(t)
	applies := h.movieApplies(from)
	require.Len(t, applies, 1, "a set equal at its paths was applied")
	assert.Contains(t, statusKeys(applies[0]), "downloads", "the catalog apply is never skipped")

	metaTitle.Store("Heat (1995)")
	from = len(h.e.StatusApplies())
	h.pass(t)
	applies = h.movieApplies(from)
	require.Len(t, applies, 2)
	assert.ElementsMatch(t, []string{"metadata"}, statusKeys(applies[1]), "only the set that changed")
	assert.Equal(t, "Heat (1995)", h.movie(t).Status.Metadata.Title)
}

// TestAStageThatPanicsKeepsItsStoredValues (ruling R5, loop §3.6): a stage
// that panics contributes nothing this pass -- the kind re-sends its stored
// downloads, still catalog's, and no set of a manager it declares fields of
// is applied, so neither its own fields nor a sibling's in that set are
// released or rolled back -- while every other stage's set lands and the
// pass retries after the planner-failed interval.
func TestAStageThatPanicsKeepsItsStoredValues(t *testing.T) {
	var panics atomic.Bool
	var searched atomic.Int64
	searched.Store(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC).Unix())
	var metaTitle atomic.Value
	metaTitle.Store("Heat")
	grab := declaringStage{&stubStage{name: remediation.StageGrab, declares: []k8s.FieldManager{k8s.ManagerCatalogGrab},
		plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
			if panics.Load() {
				panic("grab: boom")
			}
			return grabbing("Heat.1995.1080p.BluRay"), nil
		}}}
	search := &stubStage{name: remediation.StageSearch, plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
		return remediation.ItemResult{Sets: []itempass.Set{{Manager: k8s.ManagerCatalogGrab, Fields: map[string]any{
			"lastSearchedAt": metav1.NewTime(time.Unix(searched.Load(), 0).UTC()),
		}}}}, nil
	}}
	meta := &stubStage{name: remediation.StageMetadata, plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
		return describing(metaTitle.Load().(string)), nil
	}}
	h := newItemHarness(t, "stagepanic", grab, search, meta)
	h.pass(t)
	steady := h.movie(t)
	require.Len(t, steady.Status.Downloads, 1, "setup")
	require.NotNil(t, steady.Status.PendingGrab, "setup")
	require.NotNil(t, steady.Status.LastSearchedAt, "setup")

	panics.Store(true)
	searched.Add(3600)
	metaTitle.Store("Heat (1995)")
	res := h.pass(t)
	assert.Equal(t, 10*time.Minute, res.RequeueAfter, "a failed stage retries after plannerFailedRetry")
	got := h.movie(t)
	assert.Equal(t, steady.Status.Downloads, got.Status.Downloads, "the stored downloads moved")
	assert.True(t, owns(got.ManagedFields, "catalog", "downloads"), "catalog released the downloads")
	assert.Equal(t, steady.Status.PendingGrab, got.Status.PendingGrab, "the panicking stage's field moved")
	assert.True(t, owns(got.ManagedFields, "catalog-grab", "pendingGrab"), "catalog-grab released the panicking stage's field")
	assert.Equal(t, steady.Status.LastSearchedAt, got.Status.LastSearchedAt, "a set missing the failed stage's fields was applied")
	assert.Equal(t, "Heat (1995)", got.Status.Metadata.Title, "another stage's set did not land")

	panics.Store(false)
	h.pass(t)
	got = h.movie(t)
	assert.Equal(t, time.Unix(searched.Load(), 0).UTC(), got.Status.LastSearchedAt.UTC(), "the next clean pass applies the set")
}
