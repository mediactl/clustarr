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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
)

// TestAnItemApplyThatWouldChangeNothingIsNotSent: with an ApplyMemo, a pass
// over an item at rest -- the same declaration, the item at the
// resourceVersion that declaration's apply left -- sends no catalog apply,
// for each kind that owns grab entries; the pass goes on to its sets and
// effects as if it had landed. A new declaration is sent, and so is the same
// one once anything else has written the item, since only the item as the
// apply left it is known unchanged (the cache strips managedFields). On
// talos (2026-11-26) ~580 Albums waiting for a metadata slot were each
// applied every 10 s, unchanged, ~40 applies a second.
func TestAnItemApplyThatWouldChangeNothingIsNotSent(t *testing.T) {
	e := remediationtest.Start(t)
	bus := &fullQueueBus{Bus: e.Bus}
	e.Loop.Bus = bus
	scheme := e.Admin.Scheme()
	for _, ok := range ownerKinds() {
		t.Run(string(ok.kind), func(t *testing.T) {
			ns := "memo-" + strings.ToLower(string(ok.kind))
			remediationtest.MustNamespace(t, t.Context(), e.Admin, ns)
			obj := ok.obj(ns, "owner")
			require.NoError(t, e.Admin.Create(t.Context(), obj))
			var contribution atomic.Pointer[itempass.Contribution]
			stage := &stubStage{name: remediation.StageDownloads, plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
				if c := contribution.Load(); c != nil {
					return remediation.ItemResult{Contribution: *c}, nil
				}
				return remediation.ItemResult{}, nil
			}}
			ir := &remediation.ItemReconciler{
				Items:  map[remediation.KeyKind]rollup.Item{ok.kind: ok.item(e.Client(), scheme, bus)},
				Stages: []remediation.ItemStage{stage}, Env: e.Loop, Client: e.Client(),
				Applies: remediation.NewApplyMemo(),
			}
			// sent is how many catalog applies one pass sent.
			sent := func(t *testing.T, what string) int {
				t.Helper()
				before := appliesTo(e, ok.kind, "owner")
				_, err := ir.Reconcile(t.Context(), remediation.Key{Kind: ok.kind, Namespace: ns, Name: "owner"})
				require.NoError(t, err, what)
				return appliesTo(e, ok.kind, "owner") - before
			}
			// rest runs passes until one sends nothing: the cache has caught
			// up with the last apply and the declaration no longer moves.
			rest := func(t *testing.T, what string) {
				t.Helper()
				require.Eventually(t, func() bool { return sent(t, what) == 0 }, 20*time.Second, 50*time.Millisecond,
					"%s: the item never came to rest", what)
			}

			rest(t, "the first passes")
			for range 3 {
				require.Zero(t, sent(t, "at rest"), "a pass that changes nothing sent an apply")
			}

			// A new declaration is sent, once, and the item rests on it.
			entries := []catalogv1alpha1.DownloadEntry{itemtest.Entry("e1", commonv1.DownloadPhaseDownloading)}
			contribution.Store(&itempass.Contribution{Downloads: &entries})
			require.Equal(t, 1, sent(t, "new entries"), "a changed declaration is applied")
			rest(t, "the new entries")
			got, _ := downloadsOf(t, e, obj)
			require.Len(t, got, 1, "the new declaration landed")
			require.Zero(t, sent(t, "at rest on the new entries"))

			// Anything else writing the item makes it unknown again: the same
			// declaration is sent once more, then known.
			patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"touched":"yes"}}}`))
			require.NoError(t, e.Admin.Patch(t.Context(), obj.DeepCopyObject().(client.Object), patch))
			require.Eventually(t, func() bool {
				cached := obj.DeepCopyObject().(client.Object)
				return e.Client().Get(t.Context(), client.ObjectKeyFromObject(obj), cached) == nil && cached.GetLabels()["touched"] == "yes"
			}, 10*time.Second, 20*time.Millisecond, "the cache never saw the outside write")
			assert.Equal(t, 1, sent(t, "after an outside write"), "an item something else wrote is applied again")
			rest(t, "after the outside write")
			require.Zero(t, sent(t, "at rest again"))
		})
	}
}
