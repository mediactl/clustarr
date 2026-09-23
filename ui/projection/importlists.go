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

package projection

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// ImportListEntry is one row on the Import Lists page (amendment §A3.4,
// Task G3-4): "each list, its schedule, last sync, item counts, and the
// Trakt device-code flow when authorization is pending". The page is
// read-only -- the spec fields an operator would edit (enabled, sync level,
// refresh interval) live in the Settings page's editable set once that
// grows to cover import lists, not here -- so this type carries only what
// the page displays.
type ImportListEntry struct {
	// Ref identifies the ImportList.
	Ref types.NamespacedName

	// Kinds is spec.kinds: the catalog kinds this list may add.
	Kinds []string

	// Enabled is spec.enabled, defaulting to true for a nil pointer -- every
	// ImportList's CRD default is enabled=true, so a hand-built fixture with
	// the field left nil renders the same as the CRD default.
	Enabled bool

	// SourceType names which of ImportListSpec's one-of source fields is set
	// (trakt, plex, tmdb, mdblist, stevenLu, imdbCSV, custom or arr), or ""
	// for a hand-built fixture that sets none of them -- the CRD's own
	// XValidation rule keeps a real object from ever reaching that state.
	SourceType string

	// SyncLevel is spec.syncLevel: what happens to items that fell off the
	// list.
	SyncLevel string

	// LastSyncAt is status.lastSyncAt, nil until the first sync completes.
	LastSyncAt *metav1.Time

	// NextSyncAt is status.nextSyncAt, nil until the list is scheduled.
	NextSyncAt *metav1.Time

	// ItemCount, AddedCount, ExcludedCount and RemovedCount mirror
	// ImportListStatus's own fields: how many entries the last sync saw,
	// added, excluded and removed.
	ItemCount     int32
	AddedCount    int32
	ExcludedCount int32
	RemovedCount  int32

	// Auth mirrors status.auth: the in-flight device-code authorization flow
	// (Trakt, Plex), nil when none is pending. AuthView is this package's own
	// copy of the fields the page shows -- the user code, the verification
	// URL and when the code expires -- so the page never has to reach past
	// this projection into catalogv1.DeviceAuth itself.
	Auth *AuthView

	// LastError is status.lastError: the most recent sync failure, if any.
	LastError string
}

// AuthView is the device-code flow fields the Import Lists page shows while
// authorization is pending (§A3.4: "the Trakt device-code flow when
// authorization is pending").
type AuthView struct {
	// State is status.auth.state (none, pending, authorized, expired).
	State string

	// UserCode is the code the user types at VerificationURL.
	UserCode string

	// VerificationURL is where the user goes to approve the flow.
	VerificationURL string

	// ExpiresAt is when UserCode stops being accepted.
	ExpiresAt *metav1.Time
}

// listImportLists lists every current ImportList through r -- one more List
// call added to the same tick [project] already performs (ruling R4: a new
// stream shares the one list round rather than starting a ticker of its
// own), mirroring [listLibraryScans]'s own shape.
func listImportLists(ctx context.Context, r client.Reader) ([]catalogv1.ImportList, error) {
	var lists catalogv1.ImportListList
	if err := r.List(ctx, &lists); err != nil {
		return nil, fmt.Errorf("projection: list import lists: %w", err)
	}
	return lists.Items, nil
}

// buildImportListEntries derives one [ImportListEntry] per ImportList,
// sorted by namespace then name for a stable render across ticks.
func buildImportListEntries(lists []catalogv1.ImportList) []ImportListEntry {
	out := make([]ImportListEntry, 0, len(lists))
	for i := range lists {
		out = append(out, describeImportList(&lists[i]))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref.Namespace != out[j].Ref.Namespace {
			return out[i].Ref.Namespace < out[j].Ref.Namespace
		}
		return out[i].Ref.Name < out[j].Ref.Name
	})
	return out
}

// describeImportList reads one ImportList's spec and status into an
// [ImportListEntry].
func describeImportList(l *catalogv1.ImportList) ImportListEntry {
	entry := ImportListEntry{
		Ref:           types.NamespacedName{Namespace: l.Namespace, Name: l.Name},
		Kinds:         l.Spec.Kinds,
		Enabled:       l.Spec.Enabled == nil || *l.Spec.Enabled,
		SourceType:    importListSourceType(l.Spec),
		SyncLevel:     string(l.Spec.SyncLevel),
		LastSyncAt:    l.Status.LastSyncAt,
		NextSyncAt:    l.Status.NextSyncAt,
		ItemCount:     l.Status.ItemCount,
		AddedCount:    l.Status.AddedCount,
		ExcludedCount: l.Status.ExcludedCount,
		RemovedCount:  l.Status.RemovedCount,
		LastError:     l.Status.LastError,
	}
	if l.Status.Auth != nil {
		entry.Auth = &AuthView{
			State:           string(l.Status.Auth.State),
			UserCode:        l.Status.Auth.UserCode,
			VerificationURL: l.Status.Auth.VerificationURL,
			ExpiresAt:       l.Status.Auth.ExpiresAt,
		}
	}
	return entry
}

// importListSourceType names which one-of source field spec sets, matching
// the CRD's own XValidation rule's field list. It reads "" for a hand-built
// fixture with none set, which a real object -- the rule requires exactly
// one -- never is.
func importListSourceType(spec catalogv1.ImportListSpec) string {
	switch {
	case spec.Trakt != nil:
		return "trakt"
	case spec.Plex != nil:
		return "plex"
	case spec.Tmdb != nil:
		return "tmdb"
	case spec.Mdblist != nil:
		return "mdblist"
	case spec.StevenLu != nil:
		return "stevenLu"
	case spec.ImdbCSV != nil:
		return "imdbCSV"
	case spec.Custom != nil:
		return "custom"
	case spec.Arr != nil:
		return "arr"
	default:
		return ""
	}
}
