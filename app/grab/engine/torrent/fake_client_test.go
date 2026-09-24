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

package torrent

import (
	"context"
	"crypto/sha1" //nolint:gosec // test fixture id derivation, not cryptography
	"encoding/hex"
	"sync"
	"time"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// fakeClient is a minimal, fully in-memory [download.Client] test double.
// Its id is a deterministic hash of the request's Payload/Magnet, standing in
// for a real info hash without needing a real .torrent -- this package's
// reconciler-mechanics tests only care that the SAME payload always resolves
// to the SAME id (Add's idempotency contract) and that a DIFFERENT payload
// resolves to a different one, not about real BitTorrent hashing.
type fakeClient struct {
	mu sync.Mutex

	items map[string]download.Item

	addRequests       []download.AddRequest
	pauseCalls        []string
	resumeCalls       []string
	seedCriteriaCalls []string
	priorityCalls     []priorityCall
	markImportedCalls []string
	removeCalls       []removeCall

	addErr    error
	removeErr error
}

// removeCall records one Remove invocation, id and deleteData together --
// app/grab/engine/usenet's fakeDownloadClient test double records the same
// shape for the same reason: a test asserting RemoveDataOnDelete's effect
// needs both.
type removeCall struct {
	id         string
	deleteData bool
}

// priorityCall records one SetPriority invocation.
type priorityCall struct {
	id       string
	priority downloadv1alpha1.DownloadPriority
}

func newFakeClient() *fakeClient {
	return &fakeClient{items: make(map[string]download.Item)}
}

func fakeID(req download.AddRequest) string {
	h := sha1.New() //nolint:gosec // test fixture id derivation, not cryptography
	h.Write([]byte(req.Magnet))
	h.Write(req.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

var _ download.Client = (*fakeClient)(nil)

func (f *fakeClient) Info(context.Context) (download.Info, error) { return download.Info{}, nil }

func (f *fakeClient) Add(_ context.Context, req download.AddRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return "", f.addErr
	}
	f.addRequests = append(f.addRequests, req)

	id := fakeID(req)
	if _, ok := f.items[id]; ok {
		// Idempotent: an existing transfer is returned unchanged.
		return id, nil
	}
	status := download.StatusDownloading
	stage := downloadv1alpha1.DownloadStageTransferring
	if req.Paused {
		status = download.StatusPaused
		stage = ""
	}
	addedAt := req.AddedAt
	if addedAt.IsZero() {
		addedAt = time.Now()
	}
	f.items[id] = download.Item{
		ID:          id,
		Status:      status,
		Stage:       stage,
		ContentRoot: "/data/torrents/" + req.Category + "/" + req.Name,
		Files:       []download.File{{Path: req.Name + ".bin", SizeBytes: 100}},
		TotalBytes:  100,
		AddedAt:     addedAt,
	}
	return id, nil
}

func (f *fakeClient) Get(_ context.Context, id string) (download.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.items[id]
	if !ok {
		return download.Item{}, download.ErrNotFound
	}
	return item, nil
}

func (f *fakeClient) List(_ context.Context) ([]download.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]download.Item, 0, len(f.items))
	for _, item := range f.items {
		out = append(out, item)
	}
	return out, nil
}

func (f *fakeClient) Pause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.items[id]
	if !ok {
		return download.ErrNotFound
	}
	f.pauseCalls = append(f.pauseCalls, id)
	item.Status = download.StatusPaused
	item.Stage = ""
	f.items[id] = item
	return nil
}

func (f *fakeClient) Resume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.items[id]
	if !ok {
		return download.ErrNotFound
	}
	f.resumeCalls = append(f.resumeCalls, id)
	item.Status = download.StatusDownloading
	item.Stage = downloadv1alpha1.DownloadStageTransferring
	f.items[id] = item
	return nil
}

func (f *fakeClient) SetPriority(_ context.Context, id string, p downloadv1alpha1.DownloadPriority) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return download.ErrNotFound
	}
	f.priorityCalls = append(f.priorityCalls, priorityCall{id: id, priority: p})
	return nil
}

func (f *fakeClient) SetSeedCriteria(_ context.Context, id string, _ commonv1alpha1.SeedCriteria) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return download.ErrNotFound
	}
	f.seedCriteriaCalls = append(f.seedCriteriaCalls, id)
	return nil
}

func (f *fakeClient) MarkImported(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.items[id]
	if !ok {
		return download.ErrNotFound
	}
	f.markImportedCalls = append(f.markImportedCalls, id)
	item.CanBeRemoved = true
	f.items[id] = item
	return nil
}

func (f *fakeClient) Remove(_ context.Context, id string, deleteData bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	if _, ok := f.items[id]; !ok {
		return download.ErrNotFound
	}
	f.removeCalls = append(f.removeCalls, removeCall{id: id, deleteData: deleteData})
	delete(f.items, id)
	return nil
}

func (f *fakeClient) Close() error { return nil }

func (f *fakeClient) addCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.addRequests)
}

// fail marks id failed with reason, the way pkg/download/torrent's session
// does on a stall or a write error.
func (f *fakeClient) fail(id string, reason downloadv1alpha1.DownloadFailureReason) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item := f.items[id]
	item.Status = download.StatusFailed
	item.FailureReason = reason
	item.Message = "failed: " + string(reason)
	f.items[id] = item
}

func (f *fakeClient) removeCallsSnapshot() []removeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]removeCall(nil), f.removeCalls...)
}
