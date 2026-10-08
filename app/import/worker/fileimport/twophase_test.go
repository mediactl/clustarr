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

package fileimport_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// importRun drives one grab entry's import through its real two phases
// (ADR-0019 §6.9): importplan decides, as the manager's downloads stage
// does, and the worker inspects and executes, as the import agent does,
// the records between them on the bus's clustarr-imports.
type importRun struct {
	t   *testing.T
	w   *fileimport.Worker
	bus events.Bus
	// existing reads the covered items' MediaFiles, as the stage does
	// (mfindex's item keys); nil reads none.
	existing client.Reader
	// in is the stage's input but for the records, the import summary and
	// the existing files, which each decide fills.
	in importplan.Input
	// dec is the last decision.
	dec lifecycle.ImportDecision
}

// record reads the entry's record of phase, nil when there is none.
func (r *importRun) record(phase string) *schema.ImportRecord {
	r.t.Helper()
	rd := records.NewReader(r.bus.KV(events.BucketImports), agentrecords.Imports())
	rec, _, ok, err := rd.Get(context.Background(), agentrecords.ImportKey(r.in.Entry.UID, phase))
	require.NoError(r.t, err)
	if !ok {
		return nil
	}
	return rec
}

// decide is importplan's next decision over the records and the last
// decision's summary; mutate adjusts the input first.
func (r *importRun) decide(mutate ...func(*importplan.Input)) lifecycle.ImportDecision {
	r.t.Helper()
	in := r.in
	if r.dec.Summary.Phase != "" {
		s := *r.dec.Summary.DeepCopy()
		in.Entry.Import = &s
	}
	in.Inspect = r.record(schema.ImportSubInspect)
	in.Execute = r.record(schema.ImportSubExecute)
	in.Existing = r.existingFor(in.Inspect)
	for _, m := range mutate {
		m(&in)
	}
	r.dec = importplan.Decide(in)
	return r.dec
}

// existingFor is each inspected file's items' MediaFiles, keyed as
// importplan.ItemKey, read through mfindex's item keys.
func (r *importRun) existingFor(rec *schema.ImportRecord) map[string][]catalogv1alpha1.MediaFile {
	r.t.Helper()
	out := map[string][]catalogv1alpha1.MediaFile{}
	if r.existing == nil || rec == nil || rec.Inspect == nil {
		return out
	}
	var all catalogv1alpha1.MediaFileList
	require.NoError(r.t, r.existing.List(context.Background(), &all, client.InNamespace(r.in.Owner.Namespace)))
	for _, f := range rec.Inspect.Files {
		if f.Proposed == nil {
			continue
		}
		keys := f.Keys
		if len(keys) == 0 {
			keys = []string{f.Proposed.Name}
		}
		for _, k := range keys {
			key := importplan.ItemKey(importplan.MediaKindOf(f.Proposed.Kind), k)
			if _, done := out[key]; done {
				continue
			}
			out[key] = []catalogv1alpha1.MediaFile{}
			for i := range all.Items {
				for _, ik := range mfindex.Extract(&all.Items[i]) {
					if ik == key {
						out[key] = append(out[key], all.Items[i])
					}
				}
			}
		}
	}
	return out
}

// handle delivers task to the worker as the import consumer would.
func (r *importRun) handle(task schema.Payload) error {
	r.t.Helper()
	name, data, err := schema.Encode(task)
	require.NoError(r.t, err)
	return r.w.Handle(context.Background(), &fakeMessage{env: &events.Envelope{Schema: name, Data: data}})
}

// deliver hands the last decision's task to the worker and requires it to
// succeed.
func (r *importRun) deliver() {
	r.t.Helper()
	require.NotNil(r.t, r.dec.Dispatch, "the decision owes no task: %+v", r.dec.Summary)
	task, ok := r.dec.Dispatch.Task.(schema.Payload)
	require.True(r.t, ok, "the task %T is not a payload", r.dec.Dispatch.Task)
	require.NoError(r.t, r.handle(task))
}

// executeTask is the last decision's execute task.
func (r *importRun) executeTask() *schema.ImportExecuteTask {
	r.t.Helper()
	require.NotNil(r.t, r.dec.Dispatch, "the decision owes no task: %+v", r.dec.Summary)
	require.Equal(r.t, schema.ImportSubExecute, r.dec.Dispatch.Phase, "%+v", r.dec.Summary)
	task, ok := r.dec.Dispatch.Task.(*schema.ImportExecuteTask)
	require.True(r.t, ok)
	return task
}
