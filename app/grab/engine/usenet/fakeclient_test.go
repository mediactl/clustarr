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

package usenet_test

import (
	"context"
	"fmt"
	"sync"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// removeCall records one Remove invocation, id and deleteData both, so a test
// can assert the engine resolved spec.removeDataOnDelete/spec.removeOnImport
// correctly rather than just that Remove happened.
type removeCall struct {
	id         string
	deleteData bool
}

// fakeDownloadClient is a download.Client test double: an in-memory map of
// items plus a call log for every mutating method, so
// app/grab/engine/usenet's Reconciler can be exercised without a real NNTP
// pool. It is intentionally NOT the concrete pkg/download/usenet.Client --
// the Reconciler depends only on the download.Client interface, and
// integration_envtest_test.go is where the real client (and a real NNTP
// stub) proves the seam that matters is wired correctly.
type fakeDownloadClient struct {
	mu sync.Mutex

	items map[string]download.Item

	addCalls         []download.AddRequest
	addErr           error
	pauseCalls       []string
	resumeCalls      []string
	priorityCalls    []downloadv1alpha1.DownloadPriority
	markImportedIDs  []string
	removeCalls      []removeCall
	removeErr        error
	nextIDCallsCount int
}

func newFakeDownloadClient() *fakeDownloadClient {
	return &fakeDownloadClient{items: map[string]download.Item{}}
}

var _ download.Client = (*fakeDownloadClient)(nil)

func (f *fakeDownloadClient) Info(context.Context) (download.Info, error) {
	return download.Info{}, nil
}

func (f *fakeDownloadClient) Add(_ context.Context, req download.AddRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addCalls = append(f.addCalls, req)
	if f.addErr != nil {
		return "", f.addErr
	}
	f.nextIDCallsCount++
	id := fmt.Sprintf("id-%d", f.nextIDCallsCount)
	status := download.StatusDownloading
	if req.Paused {
		status = download.StatusPaused
	}
	f.items[id] = download.Item{
		ID:              id,
		Status:          status,
		Stage:           "transferring",
		TotalBytes:      int64(len(req.Payload)) * 10,
		DownloadedBytes: 0,
	}
	return id, nil
}

func (f *fakeDownloadClient) Get(_ context.Context, id string) (download.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[id]
	if !ok {
		return download.Item{}, download.ErrNotFound
	}
	return it, nil
}

func (f *fakeDownloadClient) List(context.Context) ([]download.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]download.Item, 0, len(f.items))
	for _, it := range f.items {
		out = append(out, it)
	}
	return out, nil
}

func (f *fakeDownloadClient) Pause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauseCalls = append(f.pauseCalls, id)
	it, ok := f.items[id]
	if !ok {
		return download.ErrNotFound
	}
	// Like pkg/download/usenet: pausing a health-paused job is the
	// operator acknowledging it.
	it.Status = download.StatusPaused
	it.HealthPaused = false
	f.items[id] = it
	return nil
}

func (f *fakeDownloadClient) Resume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeCalls = append(f.resumeCalls, id)
	it, ok := f.items[id]
	if !ok {
		return download.ErrNotFound
	}
	if it.HealthPaused {
		// Like pkg/download/usenet: Resume leaves a health pause alone.
		return nil
	}
	it.Status = download.StatusDownloading
	f.items[id] = it
	return nil
}

func (f *fakeDownloadClient) SetPriority(_ context.Context, id string, p downloadv1alpha1.DownloadPriority) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return download.ErrNotFound
	}
	f.priorityCalls = append(f.priorityCalls, p)
	return nil
}

func (f *fakeDownloadClient) SetSeedCriteria(context.Context, string, commonv1alpha1.SeedCriteria) error {
	return nil
}

func (f *fakeDownloadClient) MarkImported(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markImportedIDs = append(f.markImportedIDs, id)
	if _, ok := f.items[id]; !ok {
		return download.ErrNotFound
	}
	return nil
}

func (f *fakeDownloadClient) Remove(_ context.Context, id string, deleteData bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls = append(f.removeCalls, removeCall{id: id, deleteData: deleteData})
	if f.removeErr != nil {
		return f.removeErr
	}
	if _, ok := f.items[id]; !ok {
		return download.ErrNotFound
	}
	delete(f.items, id)
	return nil
}

func (f *fakeDownloadClient) Close() error { return nil }

// setItem seeds an item directly, for a test simulating an id the client
// already knew about before this Reconcile call -- re-attach, or a status
// field set by an earlier reconcile in the same test.
func (f *fakeDownloadClient) setItem(it download.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[it.ID] = it
}
