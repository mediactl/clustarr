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
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/issue"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// fileRecorder captures the media-file events a reconciler publishes
// (clustarr.evt.catalog.mediafile.*) and ignores everything else it
// publishes: item events, metadata tasks.
type fileRecorder struct {
	mu       sync.Mutex
	subjects []string
	envs     []*events.Envelope
}

func (p *fileRecorder) Publish(_ context.Context, subject string, env *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	if strings.HasPrefix(subject, "clustarr.evt.catalog.mediafile.") {
		p.mu.Lock()
		p.subjects = append(p.subjects, subject)
		p.envs = append(p.envs, env)
		p.mu.Unlock()
	}
	return events.Receipt{}, nil
}

func (p *fileRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.envs)
}

func (p *fileRecorder) at(t *testing.T, i int) (string, *events.Envelope, schema.MediaFileEvent) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Greater(t, len(p.envs), i)
	var evt schema.MediaFileEvent
	require.NoError(t, schema.Decode(p.envs[i].Schema, p.envs[i].Data, &evt))
	return p.subjects[i], p.envs[i], evt
}

// TestIssueReconcilerPublishesMediaFileEvents: spec §5's
// clustarr.evt.catalog.mediafile.<imported|deleted>.<uid> on the edges of
// status.fileRef, as the Movie and Episode reconcilers publish them, and
// nothing on a settled reconcile.
func TestIssueReconcilerPublishesMediaFileEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startIssueCache(t, ctx, cfg)
	const ns = "issue-mfevent-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))

	iss := &catalogv1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "saga-001.0", Namespace: ns}, Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "1", CalculatedNumberCentis: 100}}
	require.NoError(t, c.Create(ctx, iss))
	key := client.ObjectKeyFromObject(iss)
	require.Eventually(t, func() bool { return c.Get(ctx, key, &catalogv1alpha1.Issue{}) == nil }, 5*time.Second, 10*time.Millisecond)

	pub := &fileRecorder{}
	r := &issue.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: pub}
	reconcileOnce := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		require.NoError(t, err)
	}
	var live catalogv1alpha1.Issue
	fileRefRecorded := func(want string) {
		t.Helper()
		require.Eventually(t, func() bool {
			return c.Get(ctx, key, &live) == nil && ptr.Deref(live.Status.FileRef, "") == want
		}, 5*time.Second, 10*time.Millisecond, "status.fileRef never became %q", want)
	}

	reconcileOnce()
	assert.Equal(t, 0, pub.count(), "an issue with no file announces no file")

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "saga-001-cbz", Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: iss.Name},
			Path:     "/data/media/comics/Saga (2012)/Saga 001 (2012).cbz",
			Quality:  commonv1.Quality{Name: "CBZ"},
		},
	}
	require.NoError(t, c.Create(ctx, mf))
	require.Eventually(t, func() bool {
		var mfs catalogv1alpha1.MediaFileList
		return c.List(ctx, &mfs, client.InNamespace(ns)) == nil && len(mfs.Items) == 1
	}, 5*time.Second, 10*time.Millisecond)
	reconcileOnce()
	require.Equal(t, 1, pub.count())
	require.NoError(t, c.Get(ctx, key, &live))
	uid := string(live.UID)
	subject, env, evt := pub.at(t, 0)
	assert.Equal(t, events.CatalogMediaFileSubject(events.ActionImported, uid), subject)
	assert.Equal(t, uid+":mediafile:imported:saga-001-cbz", env.ID)
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: iss.Name}, evt.Media)
	assert.Equal(t, mf.Spec.Path, evt.ImportedPath)
	fileRefRecorded("saga-001-cbz")

	reconcileOnce()
	assert.Equal(t, 1, pub.count(), "a settled reconcile must announce nothing")

	require.NoError(t, c.Delete(ctx, mf))
	require.Eventually(t, func() bool {
		var mfs catalogv1alpha1.MediaFileList
		return c.List(ctx, &mfs, client.InNamespace(ns)) == nil && len(mfs.Items) == 0
	}, 5*time.Second, 10*time.Millisecond)
	reconcileOnce()
	require.Equal(t, 2, pub.count())
	subject, env, _ = pub.at(t, 1)
	assert.Equal(t, events.CatalogMediaFileSubject(events.ActionDeleted, uid), subject)
	assert.Equal(t, uid+":mediafile:deleted:saga-001-cbz", env.ID)
	fileRefRecorded("")
}
