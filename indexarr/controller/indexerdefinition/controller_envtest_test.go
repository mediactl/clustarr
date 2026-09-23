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

package indexerdefinition_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/controller/indexerdefinition"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testCfg is the shared control plane. It is nil when KUBEBUILDER_ASSETS is
// unset, in which case every envtest in this package SKIPS -- and a suite that
// finishes in milliseconds skipped rather than passed.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	c, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

// validYAML is a schema-valid Cardigann v11 definition. Its caps deliberately
// declare three modes, because the mode names the CRD stores are Torznab's
// wire values ("tvsearch", "movie") and NOT Cardigann's own spelling
// ("tv-search", "movie-search"): a controller that copied the keys through
// would produce a caps gate that never matches and an indexer that is never
// queried, with nothing to see in any log.
const validYAML = `
id: synthetic-tracker
name: Synthetic Tracker
description: "test fixture"
language: en-US
type: semi-private
replaces: [synthetic-old, synthetic-older]
encoding: UTF-8
links: ["https://example.invalid/"]
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: "Movies"}
    - {id: 2, cat: TV, desc: "TV"}
  modes:
    search: [q]
    tv-search: [q, season, ep]
    movie-search: [q, imdbid]
search:
  path: browse
  rows: {selector: "tr"}
  fields:
    title: {selector: "td.title"}
    size: {selector: "td.size"}
    seeders: {selector: "td.seeders"}
    category: {text: "1"}
    download: {selector: "td.title a", attribute: href}
`

// invalidYAML parses as YAML but fails the bundled v11 schema: every one of
// caps, description, encoding, links, search and type is required.
const invalidYAML = `
id: synthetic-tracker
name: Synthetic Tracker
`

func reconcileOnce(t *testing.T, r *indexerdefinition.Reconciler, name string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
}

func TestIndexerDefinitionReportsValidAndSummary(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	const name = "reports-summary"
	def := &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: validYAML},
	}
	require.NoError(t, c.Create(ctx, def))
	t.Cleanup(func() { _ = c.Delete(ctx, def) })

	r := indexerdefinition.NewReconciler(c, events.NewFakeRecorder(10))
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	var got indexv1alpha1.IndexerDefinition
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, &got))

	assert.Equal(t, got.Generation, got.Status.ObservedGeneration)
	valid := k8s.FindCondition(got.Status.Conditions, indexv1alpha1.IndexerDefinitionConditionValid)
	require.NotNil(t, valid, "no Valid condition: %+v", got.Status.Conditions)
	assert.Equal(t, metav1.ConditionTrue, valid.Status)
	assert.Equal(t, got.Generation, valid.ObservedGeneration,
		"the Valid condition does not carry the generation it was decided from")

	assert.Equal(t, "synthetic-tracker", got.Status.ID)
	assert.Equal(t, []string{"synthetic-old", "synthetic-older"}, got.Status.Replaces,
		"status.replaces carries the definition's replaces ids, the aliases an Indexer may name")
	assert.Equal(t, "Synthetic Tracker", got.Status.Name)
	assert.Equal(t, "en-US", got.Status.Language)
	assert.Equal(t, indexv1alpha1.DefinitionTypeSemiPrivate, got.Status.Type,
		`Cardigann spells it "semi-private"; the CRD enum is "semiPrivate"`)
	assert.Equal(t, commonv1alpha1.ProtocolTorrent, got.Status.Protocol)
	assert.Len(t, got.Status.Sha256, 64, "sha256 is not a hex digest: %q", got.Status.Sha256)

	assert.Equal(t, map[string][]string{
		"search":   {"q"},
		"tvsearch": {"q", "season", "ep"},
		"movie":    {"q", "imdbid"},
	}, got.Status.Caps.Modes, "caps.modes must carry Torznab's wire mode names")
	assert.Equal(t, []int32{2000, 5000}, got.Status.Caps.Categories)
}

// TestInvalidYAMLDoesNotReleaseTheValidatedSummary is the release-on-early-
// return regression test.
//
// A blank object cannot observe a release: there is nothing to release. So the
// definition is driven to a REAL steady state by a successful reconcile first,
// and only then made invalid. Server-side apply replaces a field manager's
// ownership set on every apply, so an early return that builds a
// conditions-only apply silently releases every summary field a healthy
// reconcile had set -- and the early return is the transient path, so a
// healthy object gets gutted by a blip.
//
// It asserts the WHOLE previously-written status survives, not merely the
// field this path sets. A Phase C test that checked only its own field passed
// cleanly while watching the object be gutted.
func TestInvalidYAMLDoesNotReleaseTheValidatedSummary(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	const name = "invalid-keeps-summary"
	def := &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: validYAML},
	}
	require.NoError(t, c.Create(ctx, def))
	t.Cleanup(func() { _ = c.Delete(ctx, def) })

	r := indexerdefinition.NewReconciler(c, events.NewFakeRecorder(10))
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	var steady indexv1alpha1.IndexerDefinition
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, &steady))
	require.Equal(t, "synthetic-tracker", steady.Status.ID, "setup: the first reconcile did not populate status")
	require.NotEmpty(t, steady.Status.Caps.Modes, "setup: the first reconcile did not populate caps")
	steadySha := steady.Status.Sha256
	require.NotEmpty(t, steadySha, "setup: the first reconcile did not populate sha256")

	// Now the transient failure: an edit that no longer validates.
	steady.Spec.YAML = invalidYAML
	require.NoError(t, c.Update(ctx, &steady))

	_, err = reconcileOnce(t, r, name)
	require.Error(t, err, "an unparseable spec.yaml must be reported as a terminal error")
	require.ErrorIs(t, err, reconcile.TerminalError(nil),
		"an invalid spec must not be requeued forever: %v", err)

	var after indexv1alpha1.IndexerDefinition
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, &after))

	assert.Equal(t, "synthetic-tracker", after.Status.ID, "a transient failure released status.id")
	assert.Equal(t, []string{"synthetic-old", "synthetic-older"}, after.Status.Replaces,
		"a transient failure released status.replaces")
	assert.Equal(t, "Synthetic Tracker", after.Status.Name, "a transient failure released status.name")
	assert.Equal(t, "en-US", after.Status.Language, "a transient failure released status.language")
	assert.Equal(t, indexv1alpha1.DefinitionTypeSemiPrivate, after.Status.Type, "a transient failure released status.type")
	assert.Equal(t, commonv1alpha1.ProtocolTorrent, after.Status.Protocol, "a transient failure released status.protocol")
	assert.Equal(t, steadySha, after.Status.Sha256,
		"status.sha256 is the digest as LAST VALIDATED; it was released or overwritten with the invalid document's digest")
	assert.NotEmpty(t, after.Status.Caps.Modes, "a transient failure released caps.modes")
	assert.Equal(t, []int32{2000, 5000}, after.Status.Caps.Categories, "a transient failure released caps.categories")

	// And the failure itself is reported, against the generation that caused it.
	valid := k8s.FindCondition(after.Status.Conditions, indexv1alpha1.IndexerDefinitionConditionValid)
	require.NotNil(t, valid)
	assert.Equal(t, metav1.ConditionFalse, valid.Status)
	assert.Equal(t, k8s.ReasonInvalidSpec, valid.Reason)
	assert.Equal(t, after.Generation, valid.ObservedGeneration)
	assert.Equal(t, after.Generation, after.Status.ObservedGeneration)
}

// TestReconcileIsANoOpForAMissingDefinition covers the deletion race: the
// object is gone by the time the work item is handled.
func TestReconcileIsANoOpForAMissingDefinition(t *testing.T) {
	c := newTestClient(t)
	r := indexerdefinition.NewReconciler(c, events.NewFakeRecorder(10))
	res, err := reconcileOnce(t, r, "definitely-not-there")
	require.NoError(t, err)
	assert.Zero(t, res)
}

// TestSetupWithManagerRegisters is the wiring smoke test for Task D1-8: it
// proves the builder accepts the kind, the scheme knows it and the predicate
// and options compose, without starting a manager.
func TestSetupWithManagerRegisters(t *testing.T) {
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:  k8s.MustNewScheme(),
		Metrics: server.Options{BindAddress: "0"},
	})
	require.NoError(t, err)
	require.NoError(t, indexerdefinition.NewReconciler(mgr.GetClient(), events.NewFakeRecorder(10)).SetupWithManager(mgr))
}
