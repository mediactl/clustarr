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

package k8s

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	toolsevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// ErrAgentWrite is every write through a ReadOnly client (ADR-0019 §9.2).
var ErrAgentWrite = errors.New("k8s: agents never write the Kubernetes API (ADR-0019)")

// ReadOnly wraps c for an agent (ADR-0019 §9.2): reads pass through; Create,
// Update, Patch, Apply, Delete and DeleteAllOf, and every writer of Status()
// and SubResource(...), return ErrAgentWrite and count
// clustarr_agent_write_refused_total{verb}. A subresource's Get passes
// through. It is the runtime half of the guard whose static half is the
// agents' read-only roles; it is wired into the agent's manager options in
// A7.1, once no agent package writes (ruling R13).
func ReadOnly(c client.Client) client.Client { return readOnlyClient{Client: c} }

// refuse counts and returns the refusal of verb on obj.
func refuse(verb string, obj any) error {
	metrics.AgentWriteRefusedTotal.WithLabelValues(verb).Inc()
	return fmt.Errorf("%w: %s %T", ErrAgentWrite, verb, obj)
}

type readOnlyClient struct{ client.Client }

func (readOnlyClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	return refuse("create", obj)
}

func (readOnlyClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	return refuse("update", obj)
}

func (readOnlyClient) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	return refuse("patch", obj)
}

func (readOnlyClient) Apply(_ context.Context, obj runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
	return refuse("apply", obj)
}

func (readOnlyClient) Delete(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
	return refuse("delete", obj)
}

func (readOnlyClient) DeleteAllOf(_ context.Context, obj client.Object, _ ...client.DeleteAllOfOption) error {
	return refuse("deleteAllOf", obj)
}

func (readOnlyClient) Status() client.SubResourceWriter {
	return readOnlySubResource{name: "status"}
}

func (r readOnlyClient) SubResource(subResource string) client.SubResourceClient {
	return readOnlySubResource{name: subResource, reader: r.Client.SubResource(subResource)}
}

// readOnlySubResource refuses every subresource write; Get passes through
// to reader, which Status() has none of (a SubResourceWriter has no Get).
type readOnlySubResource struct {
	name   string
	reader client.SubResourceReader
}

func (s readOnlySubResource) Get(ctx context.Context, obj, subResource client.Object, opts ...client.SubResourceGetOption) error {
	if s.reader == nil {
		return refuse(s.name+"/get", obj)
	}
	return s.reader.Get(ctx, obj, subResource, opts...)
}

func (s readOnlySubResource) Create(_ context.Context, obj, _ client.Object, _ ...client.SubResourceCreateOption) error {
	return refuse(s.name+"/create", obj)
}

func (s readOnlySubResource) Update(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
	return refuse(s.name+"/update", obj)
}

func (s readOnlySubResource) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return refuse(s.name+"/patch", obj)
}

func (s readOnlySubResource) Apply(_ context.Context, obj runtime.ApplyConfiguration, _ ...client.SubResourceApplyOption) error {
	return refuse(s.name+"/apply", obj)
}

// DiscardingRecorder is an events.k8s.io EventRecorder that records
// nothing: an agent emits no Kubernetes Event (ADR-0019 §7.7).
type DiscardingRecorder struct{}

var _ toolsevents.EventRecorder = DiscardingRecorder{}

// Eventf implements k8s.io/client-go/tools/events.EventRecorder.
func (DiscardingRecorder) Eventf(_, _ runtime.Object, _, _, _, _ string, _ ...any) {}
