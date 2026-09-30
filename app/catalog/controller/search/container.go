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

package search

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The field indexes a container's children are listed through. They are
// registered by the author, artist and comic controllers, which run in the
// same manager as this one (app/catalog.setupControllers); the keys are
// restated here because those packages keep theirs unexported.
const (
	bookByAuthorRefIndex  = ".spec.authorRef"
	albumByArtistRefIndex = ".spec.artistRef"
	issueByComicRefIndex  = ".spec.comicRef"
)

// reconcileContainer runs a Search on an author, artist or comic
// (catalogv1alpha1.ContainerKind): it publishes no search task, it fans out
// into one child Search per monitored child that is missing or below
// cutoff -- Readarr's and Lidarr's "search monitored" -- and completes when
// they all have. Its finishedAt is this manager's: no worker writes a
// container Search.
func (r *Reconciler) reconcileContainer(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	switch s.Status.Phase {
	case "":
		return r.fanOut(ctx, s)
	case catalogv1alpha1.SearchPhaseRunning:
		return r.countChildren(ctx, s)
	default:
		return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
	}
}

// containerChild is one item a container Search searches for.
type containerChild struct {
	kind commonv1.MediaKind
	name string
}

func (r *Reconciler) fanOut(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	kids, noun, err := r.wantedChildren(ctx, s)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(kids) > catalogv1alpha1.MaxContainerChildren {
		return r.fail(ctx, s, "TooManyChildren", fmt.Sprintf(
			"%d monitored %s are missing or below cutoff, more than the %d one search fans out to; search them individually or unmonitor some",
			len(kids), noun, catalogv1alpha1.MaxContainerChildren))
	}

	owner, err := k8s.OwnerReferenceAC(s, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}
	for _, kid := range kids {
		spec := catalogac.SearchSpec().
			WithMediaRef(commonv1.MediaRef{Kind: kid.kind, Name: kid.name}).
			WithGrabBest(s.Spec.GrabBest).
			WithIndexerRefs(s.Spec.IndexerRefs...).
			WithCategories(s.Spec.Categories...).
			WithOverride(s.Spec.Override)
		if s.Spec.Limit > 0 {
			spec = spec.WithLimit(s.Spec.Limit)
		}
		child := catalogac.Search(childSearchName(s.Name, kid), s.Namespace).
			WithLabels(map[string]string{catalogv1alpha1.LabelParentSearch: s.Name}).
			WithOwnerReferences(owner).
			WithSpec(spec)
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerCatalogarr, child); err != nil {
			return ctrl.Result{}, fmt.Errorf("apply child Search for %s %s: %w", kid.kind, kid.name, err)
		}
	}

	now := metav1.NewTime(r.now())
	u := newStatusUpdate(s)
	u.startedAt = &now
	u.children = &catalogv1alpha1.SearchChildren{Total: int32(len(kids)), Running: int32(len(kids))}
	if len(kids) == 0 {
		u.phase = catalogv1alpha1.SearchPhaseCompleted
		u.finishedAt = &now
		msg := fmt.Sprintf("no monitored %s are missing or below cutoff", noun)
		k8s.MarkTrue(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, k8s.ReasonReconciled, "%s", msg)
		k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionFailed, k8s.ReasonReconciled, "search completed")
		k8s.MarkTrue(s, &u.conditions, k8s.ConditionReady, k8s.ReasonReconciled, "%s", msg)
		if err := r.apply(ctx, s, u); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
	}
	u.phase = catalogv1alpha1.SearchPhaseRunning
	msg := fmt.Sprintf("searching %d monitored %s", len(kids), noun)
	k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, "Searching", "%s", msg)
	k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionFailed, "Searching", "%s", msg)
	k8s.MarkFalse(s, &u.conditions, k8s.ConditionReady, "Searching", "%s", msg)
	if err := r.apply(ctx, s, u); err != nil {
		return ctrl.Result{}, err
	}
	r.event(s, "SearchStarted", "%s", msg)
	logging.FromContext(ctx).Info("search: container fanned out", "search", s.Name, "children", len(kids))
	return ctrl.Result{}, nil
}

// countChildren reads the children back into status.children and completes
// the Search once none is running.
func (r *Reconciler) countChildren(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	var list catalogv1alpha1.SearchList
	if err := r.Client.List(ctx, &list, client.InNamespace(s.Namespace),
		client.MatchingLabels{catalogv1alpha1.LabelParentSearch: s.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list child Searches: %w", err)
	}
	n := catalogv1alpha1.SearchChildren{Total: int32(len(list.Items))}
	for i := range list.Items {
		child := &list.Items[i]
		switch {
		case child.Status.Phase == catalogv1alpha1.SearchPhaseCompleted && autoGrabPending(child):
			// Its grab lands a reconcile after Completed (handleGrabs).
			n.Running++
		case child.Status.Phase == catalogv1alpha1.SearchPhaseCompleted:
			n.Completed++
		case child.Status.Phase == catalogv1alpha1.SearchPhaseFailed:
			n.Failed++
		default:
			n.Running++
		}
		for _, g := range child.Status.Grabbed {
			if g.DownloadRef != "" {
				n.Grabbed++
				break
			}
		}
	}

	u := newStatusUpdate(s)
	u.children = &n
	if n.Running == 0 {
		now := metav1.NewTime(r.now())
		u.phase = catalogv1alpha1.SearchPhaseCompleted
		u.finishedAt = &now
		msg := fmt.Sprintf("%d searched, %d grabbed, %d failed", n.Total, n.Grabbed, n.Failed)
		k8s.MarkTrue(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, k8s.ReasonReconciled, "%s", msg)
		k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionFailed, k8s.ReasonReconciled, "search completed")
		k8s.MarkTrue(s, &u.conditions, k8s.ConditionReady, k8s.ReasonReconciled, "%s", msg)
		r.event(s, "SearchCompleted", "%s", msg)
	}
	if err := r.apply(ctx, s, u); err != nil {
		return ctrl.Result{}, err
	}
	if n.Running == 0 {
		return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
	}
	// Each child's change reconciles this Search (Owns); the requeue is
	// the backstop.
	return ctrl.Result{RequeueAfter: SearchRunningTimeout}, nil
}

// autoGrabPending reports whether a Completed Search's grabBest pick has no
// status.grabbed entry yet -- an entry holding an error counts as done.
func autoGrabPending(s *catalogv1alpha1.Search) bool {
	best := bestApproved(s)
	if best == "" {
		return false
	}
	for _, g := range s.Status.Grabbed {
		if g.GUID == best {
			return false
		}
	}
	return true
}

// wantedChildren lists the container's monitored children that are missing
// or below cutoff, sorted by name, and the plural noun for them.
func (r *Reconciler) wantedChildren(ctx context.Context, s *catalogv1alpha1.Search) ([]containerChild, string, error) {
	ns, name := s.Namespace, s.Spec.MediaRef.Name
	var (
		kids []containerChild
		noun string
	)
	now := r.now()
	add := func(kind commonv1.MediaKind, obj client.Object) {
		if wantedChild(obj, now) {
			kids = append(kids, containerChild{kind: kind, name: obj.GetName()})
		}
	}
	switch s.Spec.MediaRef.Kind {
	case commonv1.MediaKindAuthor:
		noun = "books"
		var list catalogv1alpha1.BookList
		if err := r.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{bookByAuthorRefIndex: name}); err != nil {
			return nil, noun, fmt.Errorf("list books of author %s: %w", name, err)
		}
		for i := range list.Items {
			add(commonv1.MediaKindBook, &list.Items[i])
		}
	case commonv1.MediaKindArtist:
		noun = "albums"
		var list catalogv1alpha1.AlbumList
		if err := r.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{albumByArtistRefIndex: name}); err != nil {
			return nil, noun, fmt.Errorf("list albums of artist %s: %w", name, err)
		}
		for i := range list.Items {
			add(commonv1.MediaKindAlbum, &list.Items[i])
		}
	case commonv1.MediaKindComic:
		noun = "issues"
		var list catalogv1alpha1.IssueList
		if err := r.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{issueByComicRefIndex: name}); err != nil {
			return nil, noun, fmt.Errorf("list issues of comic %s: %w", name, err)
		}
		for i := range list.Items {
			add(commonv1.MediaKindIssue, &list.Items[i])
		}
	}
	sort.Slice(kids, func(i, j int) bool { return kids[i].name < kids[j].name })
	return kids, noun, nil
}

// wantedChild reports whether a container's child is searched: monitored,
// released by now, and missing (no file; for an album, any track without
// one) or below its profile's cutoff -- the *arrs' Missing and Cutoff Unmet,
// which list only what has come out (release date <= now). An unknown date
// counts as released: an Open Library work without one is nearly always an
// old one.
func wantedChild(obj client.Object, now time.Time) bool {
	switch o := obj.(type) {
	case *catalogv1alpha1.Book:
		var date *metav1.Time
		if o.Status.Metadata != nil {
			date = o.Status.Metadata.ReleaseDate
		}
		return monitored(o.Spec.Monitored) && released(date, now) && (!o.Status.HasFile || !o.Status.CutoffMet)
	case *catalogv1alpha1.Album:
		var date *metav1.Time
		if o.Status.Metadata != nil {
			date = o.Status.Metadata.ReleaseDate
		}
		missing := o.Status.TrackFileCount == 0 || int(o.Status.TrackFileCount) < len(o.Status.Tracks)
		return monitored(o.Spec.Monitored) && released(date, now) && (missing || !o.Status.CutoffMet)
	case *catalogv1alpha1.Issue:
		return monitored(o.Spec.Monitored) && released(o.Status.Date, now) && (!o.Status.HasFile || !o.Status.CutoffMet)
	default:
		return false
	}
}

// released reports whether date is unknown or not after now.
func released(date *metav1.Time, now time.Time) bool {
	return date == nil || !date.After(now)
}

// monitored reads a spec.monitored whose CRD default is true.
func monitored(m *bool) bool { return m == nil || *m }

// childSearchName is a child Search's name: the parent's, plus a hash of
// the child, so a repeated fan-out applies the same object.
func childSearchName(parent string, kid containerChild) string {
	return k8s.ChildName(parent, string(kid.kind)+"/"+kid.name)
}
