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

package importlist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// defaultMetadataTimeout bounds one id-resolve RPC, matching
// app/import/worker/rescan's own default for the same call.
const defaultMetadataTimeout = 10 * time.Second

// Worker handles clustarr.work.import.list.* messages: one message is one
// ImportList's sync, across every catalog kind its spec.kinds names. It
// fetches each kind's list, resolves the id each item's kind is keyed on
// through the metadata gateway, and reports the whole listed set as a
// snapshot (schema.ListSnapshot) in clustarr-importlist (ADR-0019 §7.6,
// ruling R17). It decides nothing and writes no Kubernetes object: the
// ImportList controller diffs the snapshot, applies, unmonitors or deletes
// items, recycles files and writes the list's status.
type Worker struct {
	// Client reads the ImportList.
	Client client.Reader

	// APIReader reads the provider credentials' Secret and ConfigMap and
	// the Trakt token Secret straight from the apiserver (Secrets are never
	// cached, spec 2026-10-06 §5.6). Nil means Client (a test's direct
	// client).
	APIReader client.Reader

	// Bus carries the snapshot (clustarr-importlist) and the id-resolve RPC.
	Bus events.Bus

	// HTTPClient is used by every provider that makes its own HTTP calls.
	// Nil means http.DefaultClient.
	HTTPClient *http.Client

	// TraktBaseURL and PlexBaseURL override the Trakt API and Plex Discover
	// hosts (ProviderOptions). Empty means production.
	TraktBaseURL string
	PlexBaseURL  string

	// MetadataTimeout bounds one id-resolve RPC. Zero means
	// defaultMetadataTimeout.
	MetadataTimeout time.Duration

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time
}

// The import-list worker's RBAC: reads only (ADR-0019 §9.1). It reads the
// ImportList, the Secret and ConfigMap holding a provider's credentials or
// CSV content, and the owned Trakt token Secret; it writes nothing.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importlists,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get

// NewWorker builds a Worker with the production clock, timeout and HTTP
// client.
func NewWorker(c client.Reader, bus events.Bus) *Worker {
	return &Worker{
		Client: c, Bus: bus, HTTPClient: http.DefaultClient,
		MetadataTimeout: defaultMetadataTimeout, Clock: time.Now,
	}
}

func (w *Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

// reader is what Secrets and ConfigMaps are read through: APIReader, or
// Client when there is none.
func (w *Worker) reader() client.Reader {
	if w.APIReader != nil {
		return w.APIReader
	}
	return w.Client
}

// Handle implements events.Handler.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "importlist.Worker.Handle")
	defer span.End()

	if env == nil {
		return events.Discard("import-list task has no envelope", errors.New("importlist: nil envelope"))
	}

	var task schema.ListTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("importlist: undecodable ListTask", err)
	}

	ns, name, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		ns, name = task.ListRef.Namespace, task.ListRef.Name
	}
	if name == "" {
		return events.Discard("importlist: no list name in envelope key or task",
			fmt.Errorf("key=%q listRef=%+v", env.Key, task.ListRef))
	}

	log := logging.FromContext(ctx).With("namespace", ns, "importList", name)
	ctx = logging.NewContext(ctx, log)

	var il catalogv1alpha1.ImportList
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &il); err != nil {
		if apierrors.IsNotFound(err) {
			return events.Discard("importlist: list no longer exists", err)
		}
		return fmt.Errorf("importlist: get list %s/%s: %w", ns, name, err)
	}
	if task.ListRef.UID != "" && string(il.UID) != task.ListRef.UID {
		return events.Discard("importlist: list was replaced", fmt.Errorf(
			"importlist: list %s/%s has uid %s, task was published for %s",
			ns, name, il.UID, task.ListRef.UID))
	}
	if k8s.IsDeleting(&il) {
		log.Debug("importlist: list is being deleted; skipping")
		return nil
	}
	if il.Spec.Enabled != nil && !*il.Spec.Enabled {
		log.Debug("importlist: list is disabled; skipping")
		return nil
	}

	snap := w.snapshot(ctx, &il)
	if err := writeSnapshot(ctx, w.Bus.KV(events.BucketImportList), snap); err != nil {
		return fmt.Errorf("importlist: write snapshot: %w", err)
	}

	listed := 0
	for _, k := range snap.Kinds {
		listed += len(k.Items)
	}
	if snap.Error != "" {
		log.Warn("importlist: sync reported with errors", "error", snap.Error,
			"kinds", len(snap.Kinds), "listed", listed, "needsToken", snap.NeedsToken)
	} else {
		log.Info("importlist: sync reported", "kinds", len(snap.Kinds), "listed", listed)
	}
	return nil
}
