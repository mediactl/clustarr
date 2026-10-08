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

package starttest

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// StatusManagers is the set of field managers with an entry on the status
// subresource. Values alone cannot show an over-claim (every apply forces
// ownership, CLAUDE.md), so a test that means to catch one reads this.
func StatusManagers(entries []metav1.ManagedFieldsEntry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		if e.Subresource == "status" {
			out[e.Manager] = true
		}
	}
	return out
}

// ManagersOf is the field managers each RBAC identity may write under
// (split design §5.2 as ADR-0019 §9.2 left it). The manager writes under
// every k8s.FieldManagers name but the ui's, plus k8s.DefaultFieldOwner,
// the pinned owner of a write that names none (§5.3.5). No agent, no grab
// engine and neither credential-less binary writes the Kubernetes API at
// all (ADR-0019 §9.2: agents report over NATS; their client is
// k8s.ReadOnly), so their sets are empty. The ui writes only as
// clustarr-ui.
var ManagersOf = map[string][]string{
	"manager":        managerManagers(),
	"agent-catalog":  {},
	"agent-events":   {},
	"agent-metadata": {},
	"agent-import":   {},
	"agent-index":    {},
	"agent-caption":  {},
	"grab-engine":    {},
	"markers":        {},
	"transcode":      {},
	"ui":             {string(k8s.ManagerUI)},
}

func managerManagers() []string {
	var out []string
	for _, m := range k8s.FieldManagers() {
		if m != k8s.ManagerUI {
			out = append(out, string(m))
		}
	}
	return append(out, k8s.DefaultFieldOwner)
}

// clustarrGroup reports whether group is one of clustarr's API groups.
func clustarrGroup(group string) bool { return strings.HasSuffix(group, ".clustarr.io") }

// managedSnapshot is, per object UID, its kind/namespace/name and the field
// managers its managedFields name.
type managedSnapshot map[types.UID]struct {
	what     string
	managers map[string]bool
}

// snapshotManagers lists every served clustarr kind through c, unstructured,
// and records each object's managers. A kind whose CRD the apiserver does
// not serve is skipped (a scheme can know a kind no CRD installs).
func snapshotManagers(t testing.TB, c client.Client) managedSnapshot {
	t.Helper()
	ctx := context.Background()
	out := managedSnapshot{}
	var kinds []schema.GroupVersionKind
	for gvk := range k8s.MustNewScheme().AllKnownTypes() {
		if clustarrGroup(gvk.Group) && strings.HasSuffix(gvk.Kind, "List") {
			kinds = append(kinds, gvk)
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].String() < kinds[j].String() })
	if len(kinds) == 0 {
		t.Fatal("the scheme knows no clustarr list kind; AssertWroteOnlyAs would see nothing")
	}
	for _, gvk := range kinds {
		var list unstructured.UnstructuredList
		list.SetGroupVersionKind(gvk)
		if err := c.List(ctx, &list); err != nil {
			if meta.IsNoMatchError(err) {
				continue
			}
			t.Fatalf("list %s: %v", gvk, err)
		}
		for _, obj := range list.Items {
			entry := out[obj.GetUID()]
			entry.what = strings.TrimSuffix(gvk.Kind, "List") + " " + obj.GetNamespace() + "/" + obj.GetName()
			entry.managers = map[string]bool{}
			for _, mf := range obj.GetManagedFields() {
				entry.managers[mf.Manager] = true
			}
			out[obj.GetUID()] = entry
		}
	}
	return out
}

// AssertWroteOnlyAs snapshots the managedFields of every clustarr CR through
// admin (uncached), runs run, and fails for every field manager that
// appeared on an object and is not in ManagersOf[identity]. Writes the test
// makes itself through a client that names no field owner carry this test
// binary's user-agent manager and are ignored -- so a process run in this
// binary must name its owner (the manager pins k8s.DefaultFieldOwner) for
// this to see it. Values alone cannot see an over-claim (every apply forces
// ownership, CLAUDE.md), which is why this reads managedFields.
func AssertWroteOnlyAs(t testing.TB, admin client.Client, identity string, run func()) {
	t.Helper()
	allowed, ok := ManagersOf[identity]
	if !ok {
		t.Fatalf("no identity %q in starttest.ManagersOf", identity)
	}
	ignore := strings.SplitN(rest.DefaultKubernetesUserAgent(), "/", 2)[0]
	before := snapshotManagers(t, admin)
	run()
	after := snapshotManagers(t, admin)
	for uid, obj := range after {
		var appeared []string
		for m := range obj.managers {
			if before[uid].managers[m] || m == ignore || slices.Contains(allowed, m) {
				continue
			}
			appeared = append(appeared, m)
		}
		sort.Strings(appeared)
		for _, m := range appeared {
			t.Errorf("%s gained field manager %q, which identity %s may not write under (allowed: %v)",
				obj.what, m, identity, allowed)
		}
	}
}
