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

package grab

import (
	"context"
	"fmt"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The non-video kinds are each their own grab target and status target: an
// Album, a Book, an Audiobook or a comic Issue is one release, never a pack
// narrowed by keys. Their grab configuration follows the same inheritance
// the search worker and the RSS matcher rank them under
// (catalogarr/worker/search.ReadNonVideo), so the profile a release was
// approved against is the profile its Download records:
//
//   - Album: its own qualityProfileRef override, else the Artist's; the
//     Artist's delay profile and tags.
//   - Book: its own override, else the Author's; the Author's delay profile
//     and tags. A standalone Book (no authorRef) has only its own profile and
//     no tags, so tag-matched delay profiles fall to their default.
//   - Audiobook: entirely its own -- it is standalone.
//   - Issue: the Comic's profile, delay profile and tags; an Issue carries
//     none of its own.
//
// Every kind's status carries pendingGrab, lastSearchedAt and
// searchAttempts, the same owned set as a Movie. An Issue's became whole in
// X15, when IssueStatus gained pendingGrab: until then a delayed Issue grab
// was scheduled and grabbed but the Issue could not show the wait.

type albumOps struct{}

func (albumOps) kind() commonv1.MediaKind { return commonv1.MediaKindAlbum }

func (albumOps) get(ctx context.Context, c client.Client, ns, name string) (client.Object, error) {
	var a catalogv1alpha1.Album
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (albumOps) grabContext(ctx context.Context, c client.Client, obj client.Object) (grabContext, error) {
	a, ok := obj.(*catalogv1alpha1.Album)
	if !ok {
		return grabContext{}, fmt.Errorf("grab: expected *Album, got %T", obj)
	}
	var artist catalogv1alpha1.Artist
	if err := c.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: a.Spec.ArtistRef}, &artist); err != nil {
		return grabContext{}, fmt.Errorf("grab: get artist %q for album %q: %w", a.Spec.ArtistRef, a.Name, err)
	}
	return grabContext{
		QualityProfileRef: ptr.Deref(a.Spec.QualityProfileRef, artist.Spec.QualityProfileRef),
		DelayProfileRef:   artist.Spec.DelayProfileRef,
		Tags:              artist.Spec.Tags,
	}, nil
}

func (albumOps) workerStatus(obj client.Object) workerStatus {
	a, ok := obj.(*catalogv1alpha1.Album)
	if !ok {
		return workerStatus{}
	}
	return workerStatus{
		PendingGrab:    a.Status.PendingGrab,
		LastSearchedAt: a.Status.LastSearchedAt,
		SearchAttempts: a.Status.SearchAttempts,
	}
}

func (albumOps) applyWorkerStatus(ctx context.Context, c client.Client, ns, name, resourceVersion string, ws workerStatus) error {
	status := catalogac.AlbumStatus()
	if ws.PendingGrab != nil {
		status = status.WithPendingGrab(pendingGrabAC(ws.PendingGrab))
	}
	if ws.LastSearchedAt != nil {
		status = status.WithLastSearchedAt(*ws.LastSearchedAt)
	}
	if !isZeroAttempts(ws.SearchAttempts) {
		status = status.WithSearchAttempts(ws.SearchAttempts)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
		catalogac.Album(name, ns).WithResourceVersion(resourceVersion).WithStatus(status))
	return err
}

type bookOps struct{}

func (bookOps) kind() commonv1.MediaKind { return commonv1.MediaKindBook }

func (bookOps) get(ctx context.Context, c client.Client, ns, name string) (client.Object, error) {
	var b catalogv1alpha1.Book
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (bookOps) grabContext(ctx context.Context, c client.Client, obj client.Object) (grabContext, error) {
	b, ok := obj.(*catalogv1alpha1.Book)
	if !ok {
		return grabContext{}, fmt.Errorf("grab: expected *Book, got %T", obj)
	}
	gctx := grabContext{QualityProfileRef: ptr.Deref(b.Spec.QualityProfileRef, "")}
	authorRef := ptr.Deref(b.Spec.AuthorRef, "")
	if authorRef == "" {
		return gctx, nil
	}
	var author catalogv1alpha1.Author
	if err := c.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: authorRef}, &author); err != nil {
		return grabContext{}, fmt.Errorf("grab: get author %q for book %q: %w", authorRef, b.Name, err)
	}
	if gctx.QualityProfileRef == "" {
		gctx.QualityProfileRef = author.Spec.QualityProfileRef
	}
	gctx.DelayProfileRef = author.Spec.DelayProfileRef
	gctx.Tags = author.Spec.Tags
	return gctx, nil
}

func (bookOps) workerStatus(obj client.Object) workerStatus {
	b, ok := obj.(*catalogv1alpha1.Book)
	if !ok {
		return workerStatus{}
	}
	return workerStatus{
		PendingGrab:    b.Status.PendingGrab,
		LastSearchedAt: b.Status.LastSearchedAt,
		SearchAttempts: b.Status.SearchAttempts,
	}
}

func (bookOps) applyWorkerStatus(ctx context.Context, c client.Client, ns, name, resourceVersion string, ws workerStatus) error {
	status := catalogac.BookStatus()
	if ws.PendingGrab != nil {
		status = status.WithPendingGrab(pendingGrabAC(ws.PendingGrab))
	}
	if ws.LastSearchedAt != nil {
		status = status.WithLastSearchedAt(*ws.LastSearchedAt)
	}
	if !isZeroAttempts(ws.SearchAttempts) {
		status = status.WithSearchAttempts(ws.SearchAttempts)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
		catalogac.Book(name, ns).WithResourceVersion(resourceVersion).WithStatus(status))
	return err
}

type audiobookOps struct{}

func (audiobookOps) kind() commonv1.MediaKind { return commonv1.MediaKindAudiobook }

func (audiobookOps) get(ctx context.Context, c client.Client, ns, name string) (client.Object, error) {
	var a catalogv1alpha1.Audiobook
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (audiobookOps) grabContext(_ context.Context, _ client.Client, obj client.Object) (grabContext, error) {
	a, ok := obj.(*catalogv1alpha1.Audiobook)
	if !ok {
		return grabContext{}, fmt.Errorf("grab: expected *Audiobook, got %T", obj)
	}
	return grabContext{
		QualityProfileRef: a.Spec.QualityProfileRef,
		DelayProfileRef:   a.Spec.DelayProfileRef,
		Tags:              a.Spec.Tags,
	}, nil
}

func (audiobookOps) workerStatus(obj client.Object) workerStatus {
	a, ok := obj.(*catalogv1alpha1.Audiobook)
	if !ok {
		return workerStatus{}
	}
	return workerStatus{
		PendingGrab:    a.Status.PendingGrab,
		LastSearchedAt: a.Status.LastSearchedAt,
		SearchAttempts: a.Status.SearchAttempts,
	}
}

func (audiobookOps) applyWorkerStatus(ctx context.Context, c client.Client, ns, name, resourceVersion string, ws workerStatus) error {
	status := catalogac.AudiobookStatus()
	if ws.PendingGrab != nil {
		status = status.WithPendingGrab(pendingGrabAC(ws.PendingGrab))
	}
	if ws.LastSearchedAt != nil {
		status = status.WithLastSearchedAt(*ws.LastSearchedAt)
	}
	if !isZeroAttempts(ws.SearchAttempts) {
		status = status.WithSearchAttempts(ws.SearchAttempts)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
		catalogac.Audiobook(name, ns).WithResourceVersion(resourceVersion).WithStatus(status))
	return err
}

type issueOps struct{}

func (issueOps) kind() commonv1.MediaKind { return commonv1.MediaKindIssue }

func (issueOps) get(ctx context.Context, c client.Client, ns, name string) (client.Object, error) {
	var iss catalogv1alpha1.Issue
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &iss); err != nil {
		return nil, err
	}
	return &iss, nil
}

func (issueOps) grabContext(ctx context.Context, c client.Client, obj client.Object) (grabContext, error) {
	iss, ok := obj.(*catalogv1alpha1.Issue)
	if !ok {
		return grabContext{}, fmt.Errorf("grab: expected *Issue, got %T", obj)
	}
	var comic catalogv1alpha1.Comic
	if err := c.Get(ctx, client.ObjectKey{Namespace: iss.Namespace, Name: iss.Spec.ComicRef}, &comic); err != nil {
		return grabContext{}, fmt.Errorf("grab: get comic %q for issue %q: %w", iss.Spec.ComicRef, iss.Name, err)
	}
	return grabContext{
		QualityProfileRef: comic.Spec.QualityProfileRef,
		DelayProfileRef:   comic.Spec.DelayProfileRef,
		Tags:              comic.Spec.Tags,
	}, nil
}

func (issueOps) workerStatus(obj client.Object) workerStatus {
	iss, ok := obj.(*catalogv1alpha1.Issue)
	if !ok {
		return workerStatus{}
	}
	return workerStatus{
		PendingGrab:    iss.Status.PendingGrab,
		LastSearchedAt: iss.Status.LastSearchedAt,
		SearchAttempts: iss.Status.SearchAttempts,
	}
}

func (issueOps) applyWorkerStatus(ctx context.Context, c client.Client, ns, name, resourceVersion string, ws workerStatus) error {
	status := catalogac.IssueStatus()
	if ws.PendingGrab != nil {
		status = status.WithPendingGrab(pendingGrabAC(ws.PendingGrab))
	}
	if ws.LastSearchedAt != nil {
		status = status.WithLastSearchedAt(*ws.LastSearchedAt)
	}
	if !isZeroAttempts(ws.SearchAttempts) {
		status = status.WithSearchAttempts(ws.SearchAttempts)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
		catalogac.Issue(name, ns).WithResourceVersion(resourceVersion).WithStatus(status))
	return err
}
