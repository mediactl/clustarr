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

package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The Hubs page's writes
// (docs/superpowers/specs/2026-11-16-plex-hub-layouts-design.md): a
// PlexHubLayout's spec.hubs and its reset annotation, never its status,
// which cluster-plex alone writes. Every edit is worked out here, from the
// layout read through the apiserver at the write: [HubRows] is the order
// the page shows, [EditHubs] the spec.hubs an edit leaves.

// MaxLayoutHubs is the CRD's cap on spec.hubs.
const MaxLayoutHubs = 100

// plexHubLayoutGrants are the role grants these writes need.
func plexHubLayoutGrants() []Grant {
	group := catalogv1alpha1.GroupVersion.Group
	return []Grant{
		{Group: group, Resource: "plexhublayouts", Verb: "create"},
		{Group: group, Resource: "plexhublayouts", Verb: "patch"},
		{Group: group, Resource: "plexhublayouts", Verb: "delete"},
	}
}

// PlexHubLayoutName is the resource name of a library's layout, from the
// library's title as [PlexCollectionName] names a collection: "tv-shows"
// for "TV Shows", hubs-<hash> for a title with no Latin letter or digit.
func PlexHubLayoutName(library string) string {
	return titleName(library, "hubs-")
}

// HubRef names one hub of a layout, as a spec entry does: exactly one of
// its fields is set.
type HubRef struct {
	Identifier, Collection, CollectionTitle string
}

// Key is the ref as one string a form posts: "id:<identifier>",
// "col:<PlexCollection name>" or "title:<collection title>".
func (r HubRef) Key() string {
	switch {
	case r.Identifier != "":
		return "id:" + r.Identifier
	case r.Collection != "":
		return "col:" + r.Collection
	case r.CollectionTitle != "":
		return "title:" + r.CollectionTitle
	}
	return ""
}

// ParseHubRef reads a [HubRef.Key].
func ParseHubRef(key string) (HubRef, error) {
	kind, value, ok := strings.Cut(key, ":")
	if ok && value != "" {
		switch kind {
		case "id":
			return HubRef{Identifier: value}, nil
		case "col":
			return HubRef{Collection: value}, nil
		case "title":
			return HubRef{CollectionTitle: value}, nil
		}
	}
	return HubRef{}, fmt.Errorf("%w: hub %q", ErrInvalid, key)
}

// HubRefOf is the ref of a spec entry.
func HubRefOf(e catalogv1alpha1.PlexHubEntry) HubRef {
	return HubRef{Identifier: e.Identifier, Collection: e.Collection, CollectionTitle: e.CollectionTitle}
}

// statusRef is the ref a hub Plex reports is listed under: a built-in hub
// by its identifier, a collection's by the PlexCollection that holds it,
// else by its title.
func statusRef(h catalogv1alpha1.PlexHubStatus) HubRef {
	switch {
	case h.Kind == catalogv1alpha1.PlexHubCollection && h.Collection != "":
		return HubRef{Collection: h.Collection}
	case h.Kind == catalogv1alpha1.PlexHubCollection && h.Title != "":
		return HubRef{CollectionTitle: h.Title}
	}
	return HubRef{Identifier: h.Identifier}
}

// entryNames says whether the spec entry e names the hub h Plex reports.
func entryNames(e catalogv1alpha1.PlexHubEntry, h catalogv1alpha1.PlexHubStatus) bool {
	switch {
	case e.Identifier != "":
		return e.Identifier == h.Identifier
	case e.Collection != "":
		return e.Collection == h.Collection
	case e.CollectionTitle != "":
		return h.Kind == catalogv1alpha1.PlexHubCollection && e.CollectionTitle == h.Title
	}
	return false
}

// HubRow is one hub of a library in the order a layout gives it.
type HubRow struct {
	Ref HubRef
	// Title is Plex's title of the hub, else what the entry names it by.
	Title string
	Kind  catalogv1alpha1.PlexHubKind
	// Listed is true for a hub spec.hubs lists; such a row shows the
	// spec's flags, any other Plex's.
	Listed bool
	// InPlex is true for a hub status.hubs reports.
	InPlex bool

	LibraryRecommended, Home, FriendsHome bool
}

func (r HubRow) entry() catalogv1alpha1.PlexHubEntry {
	return catalogv1alpha1.PlexHubEntry{
		Identifier: r.Ref.Identifier, Collection: r.Ref.Collection, CollectionTitle: r.Ref.CollectionTitle,
		LibraryRecommended: r.LibraryRecommended, Home: r.Home, FriendsHome: r.FriendsHome,
	}
}

// HubRows is a library's hubs in the order the layout l gives them: the
// hubs spec.hubs lists, in its order, then the hubs Plex reports
// (status.hubs) that it does not list, in Plex's order. The first
// len(spec.hubs) rows are the spec's entries, index for index. A nil
// layout has none.
func HubRows(l *catalogv1alpha1.PlexHubLayout) []HubRow {
	if l == nil {
		return nil
	}
	rows := make([]HubRow, 0, len(l.Spec.Hubs)+len(l.Status.Hubs))
	taken := make([]bool, len(l.Status.Hubs))
	for _, e := range l.Spec.Hubs {
		row := HubRow{
			Ref: HubRefOf(e), Listed: true, Kind: catalogv1alpha1.PlexHubCollection,
			LibraryRecommended: e.LibraryRecommended, Home: e.Home, FriendsHome: e.FriendsHome,
		}
		row.Title = cmpOr(e.Identifier, e.Collection, e.CollectionTitle)
		if e.Identifier != "" {
			row.Kind = catalogv1alpha1.PlexHubBuiltin
		}
		for i, h := range l.Status.Hubs {
			if taken[i] || !entryNames(e, h) {
				continue
			}
			taken[i], row.InPlex, row.Kind = true, true, h.Kind
			if h.Title != "" {
				row.Title = h.Title
			}
			break
		}
		rows = append(rows, row)
	}
	for i, h := range l.Status.Hubs {
		if taken[i] {
			continue
		}
		rows = append(rows, HubRow{
			Ref: statusRef(h), Title: cmpOr(h.Title, h.Identifier), Kind: h.Kind, InPlex: true,
			LibraryRecommended: h.LibraryRecommended, Home: h.Home, FriendsHome: h.FriendsHome,
		})
	}
	return rows
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// HubOp is what a [HubEdit] does.
type HubOp string

const (
	// HubEnsure changes nothing: it only has the layout exist, so that
	// cluster-plex reports the library's hubs in its status.
	HubEnsure HubOp = "ensure"
	// HubSetFlag turns one of a hub's three flags on or off. A collection
	// (HubRef.Collection) Plex shows no hub for yet is added last, with
	// that flag alone.
	HubSetFlag HubOp = "flag"
	// HubMove moves a hub to the position To of [HubRows].
	HubMove HubOp = "move"
	// HubAdd lists a PlexCollection as a hub: where Plex already shows
	// it, else last, on Library Recommended.
	HubAdd HubOp = "add"
	// HubRemove drops an entry from spec.hubs; the hub itself stays
	// Plex's, in Plex's order after the listed ones.
	HubRemove HubOp = "remove"
)

// The three flags of a hub, as [HubEdit.Flag] and the forms name them.
const (
	HubFlagLibraryRecommended = "libraryRecommended"
	HubFlagHome               = "home"
	HubFlagFriendsHome        = "friendsHome"
)

// HubEdit is one change to a layout's hubs.
type HubEdit struct {
	Op  HubOp
	Ref HubRef
	// Flag and On are HubSetFlag's.
	Flag string
	On   bool
	// To is HubMove's position among [HubRows], clamped to them.
	To int
}

func setFlag(e *catalogv1alpha1.PlexHubEntry, flag string, on bool) error {
	switch flag {
	case HubFlagLibraryRecommended:
		e.LibraryRecommended = on
	case HubFlagHome:
		e.Home = on
	case HubFlagFriendsHome:
		e.FriendsHome = on
	default:
		return fmt.Errorf("%w: hub flag %q", ErrInvalid, flag)
	}
	return nil
}

// EditHubs is the spec.hubs the edit e leaves the layout l with; l is not
// changed. A hub spec.hubs does not list yet is first materialised: every
// row of [HubRows] up to it (and up to the position a move reaches)
// becomes an entry carrying Plex's flags, so the order shown and the other
// hubs' visibility survive the write. The hubs after it stay unlisted,
// which keeps them in Plex's order behind the listed ones.
func EditHubs(l *catalogv1alpha1.PlexHubLayout, e HubEdit) ([]catalogv1alpha1.PlexHubEntry, error) {
	rows := HubRows(l)
	var hubs []catalogv1alpha1.PlexHubEntry
	if l != nil {
		hubs = slices.Clone(l.Spec.Hubs)
	}
	// through lists every row up to and including index i.
	through := func(i int) {
		for n := len(hubs); n <= i && n < len(rows); n++ {
			hubs = append(hubs, rows[n].entry())
		}
	}
	at := slices.IndexFunc(rows, func(r HubRow) bool { return r.Ref == e.Ref })
	if e.Op != HubEnsure && e.Ref.Key() == "" {
		return nil, fmt.Errorf("%w: no hub named", ErrInvalid)
	}

	switch e.Op {
	case HubEnsure:
	case HubSetFlag:
		if at < 0 {
			if e.Ref.Collection == "" {
				return nil, fmt.Errorf("%w: the library has no hub %q", ErrInvalid, e.Ref.Key())
			}
			through(len(rows) - 1)
			hubs = append(hubs, catalogv1alpha1.PlexHubEntry{Collection: e.Ref.Collection})
			at = len(hubs) - 1
		}
		through(at)
		if err := setFlag(&hubs[at], e.Flag, e.On); err != nil {
			return nil, err
		}
	case HubMove:
		if at < 0 {
			return nil, fmt.Errorf("%w: the library has no hub %q", ErrInvalid, e.Ref.Key())
		}
		to := min(max(e.To, 0), len(rows)-1)
		through(max(at, to))
		moved := hubs[at]
		hubs = slices.Delete(hubs, at, at+1)
		hubs = slices.Insert(hubs, to, moved)
	case HubAdd:
		if e.Ref.Collection == "" {
			return nil, fmt.Errorf("%w: only a collection is added as a hub", ErrInvalid)
		}
		if at >= 0 {
			through(at)
			break
		}
		through(len(rows) - 1)
		hubs = append(hubs, catalogv1alpha1.PlexHubEntry{Collection: e.Ref.Collection, LibraryRecommended: true})
	case HubRemove:
		if at < 0 || at >= len(hubs) {
			return nil, fmt.Errorf("%w: the layout does not list %q", ErrInvalid, e.Ref.Key())
		}
		hubs = slices.Delete(hubs, at, at+1)
	default:
		return nil, fmt.Errorf("%w: hub edit %q", ErrInvalid, e.Op)
	}
	if len(hubs) > MaxLayoutHubs {
		return nil, fmt.Errorf("%w: a layout lists at most %d hubs, and this edit needs %d", ErrInvalid, MaxLayoutHubs, len(hubs))
	}
	return hubs, nil
}

// HubLayoutWriter is what the layout writes ask of the cluster.
type HubLayoutWriter interface {
	Creator
	Patcher
}

// EditPlexHubLayout applies the edit e to the layout namespace/name. It
// reads the layout through g (the apiserver, not the page's cache, so two
// quick edits build on one another) and patches spec.hubs alone, testing
// that the spec is still at the generation read. A layout that does not
// exist yet is created for library with the edit applied to an empty one;
// library is needed only then.
func EditPlexHubLayout(
	ctx context.Context, g Getter, w HubLayoutWriter, namespace, name, library string, e HubEdit,
) error {
	ctx, span := tracing.Start(ctx, "ui.actions.EditPlexHubLayout")
	defer span.End()
	err := editPlexHubLayout(ctx, g, w, namespace, name, library, e)
	if err != nil {
		tracing.RecordError(span, err)
		return err
	}
	logging.FromContext(ctx).Info("ui action: hub layout edited", "namespace", namespace, "name", name, "op", string(e.Op))
	return nil
}

func editPlexHubLayout(
	ctx context.Context, g Getter, w HubLayoutWriter, namespace, name, library string, e HubEdit,
) error {
	if err := validPlexCollectionTarget(namespace, name); err != nil {
		return err
	}
	var l catalogv1alpha1.PlexHubLayout
	err := g.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &l)
	if apierrors.IsNotFound(err) {
		hubs, err := EditHubs(nil, e)
		if err != nil {
			return err
		}
		return createPlexHubLayout(ctx, w, namespace, name, library, hubs, nil)
	}
	if err != nil {
		return fmt.Errorf("actions: read hub layout %s/%s: %w", namespace, name, err)
	}
	if e.Op == HubEnsure {
		return nil
	}
	hubs, err := EditHubs(&l, e)
	if err != nil {
		return err
	}
	if hubs == nil {
		hubs = []catalogv1alpha1.PlexHubEntry{}
	}
	var ops []map[string]any
	// The generation, as UpdatePlexCollection tests it: cluster-plex's
	// status writes move the resourceVersion alone. The apiserver sets it
	// from 1; only a test's fake leaves it 0.
	if l.Generation > 0 {
		ops = append(ops, map[string]any{"op": "test", "path": "/metadata/generation", "value": l.Generation})
	}
	// "add" replaces a member that exists and sets one that does not.
	ops = append(ops, map[string]any{"op": "add", "path": "/spec/hubs", "value": hubs})
	raw, err := json.Marshal(ops)
	if err != nil {
		return fmt.Errorf("actions: encode hub layout patch: %w", err)
	}
	obj := &catalogv1alpha1.PlexHubLayout{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := w.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		return fmt.Errorf("actions: edit hub layout %s/%s: %w", namespace, name, err)
	}
	return nil
}

func createPlexHubLayout(
	ctx context.Context, c Creator, namespace, name, library string,
	hubs []catalogv1alpha1.PlexHubEntry, annotations map[string]string,
) error {
	if strings.TrimSpace(library) == "" {
		return fmt.Errorf("%w: a new hub layout needs its library", ErrInvalid)
	}
	l := &catalogv1alpha1.PlexHubLayout{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: annotations},
		Spec:       catalogv1alpha1.PlexHubLayoutSpec{Library: library, Hubs: hubs},
	}
	if err := c.Create(ctx, l, client.FieldOwner(FieldManager)); err != nil {
		return fmt.Errorf("actions: create hub layout %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ResetPlexHubLayout asks cluster-plex to reset the library's hub
// customization: one merge patch that sets
// [catalogv1alpha1.AnnotationPlexHubsReset] to at and clears spec.hubs, so
// the layout does not put back what the reset undoes. A layout that does
// not exist yet is created for library carrying the request.
func ResetPlexHubLayout(ctx context.Context, w HubLayoutWriter, namespace, name, library string, at time.Time) error {
	ctx, span := tracing.Start(ctx, "ui.actions.ResetPlexHubLayout")
	defer span.End()
	if err := validPlexCollectionTarget(namespace, name); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	annotations := map[string]string{catalogv1alpha1.AnnotationPlexHubsReset: at.UTC().Format(time.RFC3339)}
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": annotations},
		"spec":     map[string]any{"hubs": nil},
	})
	if err != nil {
		return fmt.Errorf("actions: encode hub reset patch: %w", err)
	}
	obj := &catalogv1alpha1.PlexHubLayout{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	err = w.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager))
	if apierrors.IsNotFound(err) {
		err = createPlexHubLayout(ctx, w, namespace, name, library, nil, annotations)
	} else if err != nil {
		err = fmt.Errorf("actions: reset hub layout %s/%s: %w", namespace, name, err)
	}
	if err != nil {
		tracing.RecordError(span, err)
		return err
	}
	logging.FromContext(ctx).Info("ui action: hub reset requested", "namespace", namespace, "name", name)
	return nil
}

// DeletePlexHubLayout deletes a layout. It has no finalizer: Plex keeps
// the hubs as they are, and cluster-plex stops managing them.
func DeletePlexHubLayout(ctx context.Context, d Deleter, namespace, name string) error {
	ctx, span := tracing.Start(ctx, "ui.actions.DeletePlexHubLayout")
	defer span.End()
	if err := validPlexCollectionTarget(namespace, name); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	obj := &catalogv1alpha1.PlexHubLayout{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := d.Delete(ctx, obj); err != nil {
		err = fmt.Errorf("actions: delete hub layout %s/%s: %w", namespace, name, err)
		tracing.RecordError(span, err)
		return err
	}
	logging.FromContext(ctx).Info("ui action: hub layout deleted", "namespace", namespace, "name", name)
	return nil
}

// EditPlexHubLayout is [EditPlexHubLayout] over the Actions' writer, which
// reads the layout through the apiserver.
func (a *Actions) EditPlexHubLayout(ctx context.Context, namespace, name, library string, e HubEdit) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	g, ok := a.w.(Getter)
	if !ok {
		return ErrNoWriter
	}
	return EditPlexHubLayout(ctx, g, a.w, namespace, name, library, e)
}

// ResetPlexHubLayout is [ResetPlexHubLayout] over the Actions' writer.
func (a *Actions) ResetPlexHubLayout(ctx context.Context, namespace, name, library string, at time.Time) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return ResetPlexHubLayout(ctx, a.w, namespace, name, library, at)
}

// DeletePlexHubLayout is [DeletePlexHubLayout] over the Actions' writer.
func (a *Actions) DeletePlexHubLayout(ctx context.Context, namespace, name string) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return DeletePlexHubLayout(ctx, a.w, namespace, name)
}
