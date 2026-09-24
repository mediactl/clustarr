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
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// FieldManager is the field manager every write in this package is made
// as. It is pkg/k8s.ManagerUI's value, restated here because ui/ never
// imports pkg/k8s (see the package doc); TestFieldManagerIsK8sManagerUI
// pins the two together.
const FieldManager = "clustarr-ui"

// LabelOrigin marks an object this package created; its value is always
// [OriginUI]. It is on every Search and LibraryScan the UI creates, and on
// nothing else.
const (
	LabelOrigin = "clustarr.io/origin"
	OriginUI    = "ui"
)

// Sentinel errors. A handler maps [ErrInvalid] to 400 and [ErrNoWriter] to
// 503; anything else is the apiserver's own error, wrapped, so
// apierrors.IsNotFound and friends still work through errors.Is/As.
var (
	// ErrInvalid is returned, before any request is sent, for an empty
	// namespace or name or a kind that is not a catalog media kind.
	ErrInvalid = errors.New("actions: invalid request")

	// ErrNoWriter is returned by an [Actions] built with no [Writer]: a ui
	// process with no cluster configured renders its pages and refuses its
	// actions.
	ErrNoWriter = errors.New("actions: no cluster writer configured")
)

// Creator is the one method [SearchNow] and [Rescan] need. A
// controller-runtime client.Client satisfies it.
type Creator interface {
	Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
}

// Patcher is the one method [SetMonitored] needs. A controller-runtime
// client.Client satisfies it.
type Patcher interface {
	Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
}

// Writer is everything this package ever asks of the cluster: create,
// patch and, since the Settings page configures its kinds (settings CRUD
// design, 2026-09-24), delete. Not Update, Apply or Status --
// config/rbac/ui_role.yaml grants no verb that would let any of those
// succeed, and ui/guard_test.go bans the calls.
type Writer interface {
	Creator
	Patcher
	Deleter
}

// monitorable is one catalog kind whose spec.monitored the UI can toggle:
// the plural resource RBAC names it by, and a constructor for the typed
// object the patch is sent through.
type monitorable struct {
	resource  string
	newObject func() client.Object
}

// monitorables is every commonv1.MediaKind -- which is exactly the set of
// catalog kinds with a top-level spec.monitored, and the set a Search's
// spec.mediaRef.kind may name. The resource names are RBAC's; the envtest
// holds them to the resource the apiserver actually routes each object to,
// and cmd/clustarr/ui_rbac_test.go holds the role to them.
var monitorables = map[commonv1.MediaKind]monitorable{
	commonv1.MediaKindMovie:     {"movies", func() client.Object { return &catalogv1alpha1.Movie{} }},
	commonv1.MediaKindSeries:    {"series", func() client.Object { return &catalogv1alpha1.Series{} }},
	commonv1.MediaKindEpisode:   {"episodes", func() client.Object { return &catalogv1alpha1.Episode{} }},
	commonv1.MediaKindArtist:    {"artists", func() client.Object { return &catalogv1alpha1.Artist{} }},
	commonv1.MediaKindAlbum:     {"albums", func() client.Object { return &catalogv1alpha1.Album{} }},
	commonv1.MediaKindAuthor:    {"authors", func() client.Object { return &catalogv1alpha1.Author{} }},
	commonv1.MediaKindBook:      {"books", func() client.Object { return &catalogv1alpha1.Book{} }},
	commonv1.MediaKindAudiobook: {"audiobooks", func() client.Object { return &catalogv1alpha1.Audiobook{} }},
	commonv1.MediaKindComic:     {"comics", func() client.Object { return &catalogv1alpha1.Comic{} }},
	commonv1.MediaKindIssue:     {"issues", func() client.Object { return &catalogv1alpha1.Issue{} }},
}

// MediaKinds returns, sorted, every kind [SearchNow] and [SetMonitored]
// accept.
func MediaKinds() []commonv1.MediaKind {
	kinds := make([]commonv1.MediaKind, 0, len(monitorables))
	for k := range monitorables {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	return kinds
}

// Grant is one RBAC permission: Verb on Resource in API group Group.
type Grant struct {
	Group    string
	Resource string
	Verb     string
}

// Grants returns every RBAC permission this package's actions need, and
// nothing else: create on searches and libraryscans, patch on each catalog
// kind in [MediaKinds] ("monitor this", §A3.2), plus -- Task G3-4, the
// Settings page -- patch on each kind [settingsGrants] (settings.go) names.
// It is a declaration, not a computation -- the envtest proves the §A3.2
// actions hit exactly their share of this on a real apiserver, and
// cmd/clustarr/ui_rbac_test.go proves ui_role.yaml grants exactly this whole
// set as its only write verbs. envtest does not enforce RBAC, so without
// that pair an action the role does not cover would pass every test and
// fail only in a cluster.
func Grants() []Grant {
	group := catalogv1alpha1.GroupVersion.Group
	grants := []Grant{
		{Group: group, Resource: "libraryscans", Verb: "create"},
		{Group: group, Resource: "searches", Verb: "create"},
	}
	for _, kind := range MediaKinds() {
		grants = append(grants, Grant{Group: group, Resource: monitorables[kind].resource, Verb: "patch"})
	}
	grants = append(grants, settingsGrants()...)
	grants = append(grants, configGrants()...)
	return grants
}

// SearchNow is "search now" (§A3.2): it creates a Search for the catalog
// item kind/name in namespace and returns it as the apiserver accepted it,
// generated name included.
//
// The Search is named "<name>-<random>" (generateName; the apiserver
// truncates a long prefix itself), carries [LabelOrigin]=[OriginUI], and
// leaves every other spec field to its CRD default -- limit 100, TTL 1h, all
// indexers. Whether the named item exists is the Search controller's
// question to answer in the Search's status, not this function's.
func SearchNow(
	ctx context.Context, c Creator, namespace string, kind commonv1.MediaKind, name string,
) (*catalogv1alpha1.Search, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SearchNow")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	search := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name + "-",
			Namespace:    namespace,
			Labels:       map[string]string{LabelOrigin: OriginUI},
		},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: kind, Name: name},
		},
	}
	if err := c.Create(ctx, search, client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: create Search for %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: search requested",
		"search", search.Name, "namespace", namespace, "kind", kind, "item", name)
	return search, nil
}

// Rescan is the "rescan" action (§A3.2): one LibraryScan of the whole
// RootFolder, [RescanPath] with no subpath.
func Rescan(
	ctx context.Context, c Creator, namespace, rootFolder string,
) (*catalogv1alpha1.LibraryScan, error) {
	return RescanPath(ctx, c, namespace, rootFolder, "")
}

// RescanPath creates a LibraryScan of rootFolder restricted to subpath, a
// folder beneath the root (Radarr's "Refresh & Scan" on one item rescans
// that item's own folder); an empty subpath scans the whole root. The
// scan carries only the origin label, so the RootFolder schedule never
// mistakes it for one of its own. A subpath that is absolute or climbs
// out of the root is [ErrInvalid] and writes nothing.
func RescanPath(
	ctx context.Context, c Creator, namespace, rootFolder, subpath string,
) (*catalogv1alpha1.LibraryScan, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.RescanPath")
	defer span.End()

	if namespace == "" || rootFolder == "" {
		err := fmt.Errorf("%w: rescan needs a namespace and a RootFolder name (got %q/%q)",
			ErrInvalid, namespace, rootFolder)
		tracing.RecordError(span, err)
		return nil, err
	}
	if subpath != "" {
		clean := path.Clean(subpath)
		if path.IsAbs(subpath) || clean == ".." || strings.HasPrefix(clean, "../") {
			err := fmt.Errorf("%w: rescan subpath %q is not a folder beneath the RootFolder", ErrInvalid, subpath)
			tracing.RecordError(span, err)
			return nil, err
		}
		subpath = clean
	}

	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: rootFolder + "-",
			Namespace:    namespace,
			Labels:       map[string]string{LabelOrigin: OriginUI},
		},
		Spec: catalogv1alpha1.LibraryScanSpec{RootFolderRef: rootFolder, Subpath: subpath},
	}
	if err := c.Create(ctx, scan, client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: create LibraryScan of RootFolder %s/%s: %w", namespace, rootFolder, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: rescan requested",
		"libraryScan", scan.Name, "namespace", namespace, "rootFolder", rootFolder, "subpath", subpath)
	return scan, nil
}

// monitoredPatch is the entire body [SetMonitored] sends. Monitored has no
// omitempty: false is the value the user asked for, not an absent one.
type monitoredPatch struct {
	Spec struct {
		Monitored bool `json:"monitored"`
	} `json:"spec"`
}

// SetMonitored is "monitor this" (§A3.2): it sets spec.monitored on the
// catalog item kind/name in namespace with a JSON merge patch made as
// [FieldManager], and returns the object as the apiserver returned it.
//
// The patch body is exactly {"spec":{"monitored":<monitored>}}. A missing
// item is the apiserver's NotFound, wrapped (apierrors.IsNotFound works);
// nothing is ever created. See the package doc for why this is a merge
// patch and not an apply.
func SetMonitored(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string, monitored bool,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetMonitored")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	var body monitoredPatch
	body.Spec.Monitored = monitored
	raw, err := json.Marshal(body)
	if err != nil {
		// Unreachable for a struct of one bool; kept so a future field
		// cannot turn a marshal failure into an empty patch.
		err = fmt.Errorf("actions: encode spec.monitored patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: set spec.monitored=%t on %s %s/%s: %w", monitored, kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: monitored set",
		"namespace", namespace, "kind", kind, "item", name, "monitored", monitored)
	return obj, nil
}

// validateItem is the shared precondition of [SearchNow] and [SetMonitored].
func validateItem(namespace string, kind commonv1.MediaKind, name string) error {
	if namespace == "" || name == "" {
		return fmt.Errorf("%w: need a namespace and a name (got %q/%q)", ErrInvalid, namespace, name)
	}
	if _, ok := monitorables[kind]; !ok {
		return fmt.Errorf("%w: %q is not a catalog media kind (want one of %v)", ErrInvalid, kind, MediaKinds())
	}
	return nil
}

// Actions is how the rest of ui/ reaches this package: the three actions as
// methods, over a [Writer] it holds unexported. A handler holding an
// *Actions can search, rescan and toggle monitored, and has no way to reach
// Create or Patch for anything else.
//
// A nil *Actions, or one built from a nil Writer, is valid and returns
// [ErrNoWriter] from every method.
type Actions struct {
	w Writer
}

// New returns an [Actions] over w. cmd/clustarr builds w from a
// controller-runtime client.Client; tests pass a fake.
func New(w Writer) *Actions {
	return &Actions{w: w}
}

// SearchNow is [SearchNow] over the Actions' writer.
func (a *Actions) SearchNow(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string,
) (*catalogv1alpha1.Search, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SearchNow(ctx, a.w, namespace, kind, name)
}

// Rescan is [Rescan] over the Actions' writer.
func (a *Actions) Rescan(ctx context.Context, namespace, rootFolder string) (*catalogv1alpha1.LibraryScan, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return Rescan(ctx, a.w, namespace, rootFolder)
}

// RescanPath is [RescanPath] over the Actions' writer.
func (a *Actions) RescanPath(ctx context.Context, namespace, rootFolder, subpath string) (*catalogv1alpha1.LibraryScan, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return RescanPath(ctx, a.w, namespace, rootFolder, subpath)
}

// SetMonitored is [SetMonitored] over the Actions' writer.
func (a *Actions) SetMonitored(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string, monitored bool,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetMonitored(ctx, a.w, namespace, kind, name, monitored)
}
