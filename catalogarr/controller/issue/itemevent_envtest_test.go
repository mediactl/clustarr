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

package issue_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/issue"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// itemRecorder is a Publisher that keeps every catalog item event it is
// handed, and accepts everything else (a MetadataTask) without keeping it.
type itemRecorder struct {
	mu       sync.Mutex
	subjects []string
	envs     []*events.Envelope
}

func (p *itemRecorder) Publish(_ context.Context, subject string, env *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	if strings.HasPrefix(subject, "clustarr.evt.catalog.issue.") {
		p.mu.Lock()
		p.subjects = append(p.subjects, subject)
		p.envs = append(p.envs, env)
		p.mu.Unlock()
	}
	return events.Receipt{}, nil
}

func (p *itemRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.envs)
}

func (p *itemRecorder) at(t *testing.T, i int) (string, *events.Envelope, schema.ItemEvent) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Greater(t, len(p.envs), i)
	var evt schema.ItemEvent
	require.NoError(t, schema.Decode(p.envs[i].Schema, p.envs[i].Data, &evt))
	return p.subjects[i], p.envs[i], evt
}

// startIssueCache starts a manager's cache with the MediaFile index the
// Issue reconciler lists through, and no controller.
func startIssueCache(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.MediaFile{}, ".spec.mediaRef.issue",
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindIssue {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}))
	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &downloadv1alpha1.Download{}, ".spec.target.issue",
		func(o client.Object) []string {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok || dl.Spec.Target.Kind != commonv1.MediaKindIssue {
				return nil
			}
			return []string{dl.Spec.Target.Name}
		}))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

// TestIssueReconcilerPublishesItemEvents: spec §5's
// clustarr.evt.catalog.issue.<added|updated|deleted>.<uid>, once per edge --
// added on the first reconcile, updated on a new spec generation, deleted
// before the finalizer comes off -- and nothing on a settled reconcile.
func TestIssueReconcilerPublishesItemEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startIssueCache(t, ctx, cfg)
	const ns = "issue-itemevent-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))

	obj := &catalogv1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "saga-001.0", Namespace: ns}, Spec: catalogv1alpha1.IssueSpec{ComicRef: "nobody", Number: "1", CalculatedNumberCentis: 100}}
	require.NoError(t, c.Create(ctx, obj))
	key := client.ObjectKeyFromObject(obj)
	require.Eventually(t, func() bool { return c.Get(ctx, key, &catalogv1alpha1.Issue{}) == nil }, 5*time.Second, 10*time.Millisecond)

	pub := &itemRecorder{}
	r := &issue.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: pub}
	reconcileOnce := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		require.NoError(t, err)
	}

	reconcileOnce()
	require.Equal(t, 1, pub.count())
	var live catalogv1alpha1.Issue
	require.NoError(t, c.Get(ctx, key, &live))
	uid := string(live.UID)
	subject, env, evt := pub.at(t, 0)
	assert.Equal(t, events.CatalogItemSubject("issue", events.ActionAdded, uid), subject)
	assert.Equal(t, uid+":added", env.ID)
	assert.Equal(t, events.ActionAdded, evt.Action)
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: key.Name}, evt.Media)
	assert.Equal(t, schema.Ref{Namespace: ns, Name: key.Name, UID: uid}, evt.Ref)
	assert.Equal(t, map[string]string(nil), evt.IDs)
	assert.True(t, evt.Monitored)

	// Settled: the first reconcile recorded observedGeneration, so the next
	// announces nothing.
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &live) == nil && live.Status.ObservedGeneration == live.Generation
	}, 5*time.Second, 10*time.Millisecond, "the first reconcile never recorded observedGeneration")
	reconcileOnce()
	assert.Equal(t, 1, pub.count(), "a settled reconcile must announce nothing")

	// A spec edit is an update, announced once for its generation.
	live.Spec.Monitored = new(bool)
	require.NoError(t, c.Update(ctx, &live))
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Issue
		return c.Get(ctx, key, &got) == nil && got.Generation == live.Generation && got.Spec.Monitored != nil
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, c.Get(ctx, key, &live))
	reconcileOnce()
	require.Equal(t, 2, pub.count())
	subject, env, evt = pub.at(t, 1)
	assert.Equal(t, events.CatalogItemSubject("issue", events.ActionUpdated, uid), subject)
	assert.Equal(t, uid+":updated:2", env.ID)
	assert.False(t, evt.Monitored)

	// Deleted, before the finalizer is removed.
	require.NoError(t, c.Delete(ctx, &live))
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Issue
		return c.Get(ctx, key, &got) == nil && !got.DeletionTimestamp.IsZero()
	}, 5*time.Second, 10*time.Millisecond)
	reconcileOnce()
	require.Equal(t, 3, pub.count())
	subject, env, evt = pub.at(t, 2)
	assert.Equal(t, events.CatalogItemSubject("issue", events.ActionDeleted, uid), subject)
	assert.Equal(t, uid+":deleted", env.ID)
	assert.Equal(t, events.ActionDeleted, evt.Action)
}
