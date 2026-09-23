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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/fsops"
)

// descriptor is everything [Engine.ReAttach] needs to reconstruct a
// [download.AddRequest] after a restart, other than the metainfo bytes
// themselves -- see doc.go's "Persistence" section for why those live in a
// sibling ".torrent" file instead of here.
type descriptor struct {
	Name             string                            `json:"name"`
	Category         string                            `json:"category"`
	Magnet           string                            `json:"magnet,omitempty"`
	ExpectedInfoHash string                            `json:"expectedInfoHash,omitempty"`
	Priority         downloadv1alpha1.DownloadPriority `json:"priority,omitempty"`
	Paused           bool                              `json:"paused"`
	SeedCriteria     *commonv1alpha1.SeedCriteria      `json:"seedCriteria,omitempty"`

	// Selection is the file selection resolved from the catalog at the
	// first Add ([resolveSelection]); nil wants every file. It is persisted
	// rather than re-resolved because re-attach runs before the manager
	// starts, and because the catalog may have moved on -- an episode
	// imported since, say -- while the transfer's own on-disk pieces were
	// chosen by the original selection.
	Selection *Selection `json:"selection,omitempty"`

	// AddedAt is the transfer's first-added time ([download.Item.AddedAt]),
	// handed back to the client on re-attach so the orphan reaper ages the
	// transfer from when it was really added, not from the restart. Zero in
	// a descriptor written before it existed.
	AddedAt time.Time `json:"addedAt,omitzero"`
}

// torrentFileName and sidecarFileName are the two files [saveDescriptor]
// writes per id and [loadDescriptors] reads back, both inside the state
// directory an [Engine] is constructed with.
func torrentFileName(stateDir, id string) string { return filepath.Join(stateDir, id+".torrent") }
func sidecarFileName(stateDir, id string) string { return filepath.Join(stateDir, id+".json") }

// saveDescriptor persists everything needed to re-add id after a restart.
// Both files are written through fsops.AtomicWrite, per CLAUDE.md's
// convention that a filesystem write which must survive a crash goes through
// it: a torrent added, crashed on, and never fully persisted must not
// re-attach from a half-written .torrent or sidecar.
func saveDescriptor(stateDir, id string, payload []byte, d descriptor) error {
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return fmt.Errorf("torrent: create state dir %s: %w", stateDir, err)
	}
	if len(payload) > 0 {
		if err := fsops.AtomicWrite(torrentFileName(stateDir, id), bytes.NewReader(payload), 0o640); err != nil {
			return fmt.Errorf("torrent: persist metainfo for %s: %w", id, err)
		}
	}
	body, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("torrent: marshal descriptor for %s: %w", id, err)
	}
	if err := fsops.AtomicWrite(sidecarFileName(stateDir, id), bytes.NewReader(body), 0o640); err != nil {
		return fmt.Errorf("torrent: persist descriptor for %s: %w", id, err)
	}
	return nil
}

// removeDescriptor deletes both of id's persisted files. Missing files are
// not an error: a caller may remove a descriptor that only ever existed as a
// magnet (no .torrent file) or one that was already cleaned up.
func removeDescriptor(stateDir, id string) error {
	var errs []error
	for _, path := range []string{torrentFileName(stateDir, id), sidecarFileName(stateDir, id)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("torrent: remove descriptor for %s: %w", id, errors.Join(errs...))
	}
	return nil
}

// loadedDescriptor pairs a decoded [descriptor] with the metainfo bytes read
// from its sibling .torrent file, when one exists.
type loadedDescriptor struct {
	ID      string
	Desc    descriptor
	Payload []byte
}

// updateDescriptorState refreshes the mutable half of id's persisted
// descriptor -- Paused and SeedCriteria, the two fields [Reconciler.sync]
// can change after the initial Add -- without touching the payload/magnet a
// re-resolve would otherwise require. It is a no-op, reporting ok=false,
// when no descriptor is on disk for id (nothing to refresh: either it was
// never persisted, e.g. a re-attach anomaly, or it has already been removed).
//
// Without this, a Download paused or re-scored after its initial Add would
// re-attach in its ORIGINAL state after an engine restart, relying on the
// next sync() pass to self-correct -- a brief but real window where a
// transfer marked paused in spec.paused runs unpaused until the very next
// reconcile notices. Keeping the descriptor current removes that window
// rather than tolerating it.
func updateDescriptorState(stateDir, id string, paused bool, seedCriteria *commonv1alpha1.SeedCriteria) error {
	body, err := os.ReadFile(sidecarFileName(stateDir, id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("torrent: read descriptor for %s: %w", id, err)
	}
	var d descriptor
	if err := json.Unmarshal(body, &d); err != nil {
		return fmt.Errorf("torrent: decode descriptor for %s: %w", id, err)
	}
	if d.Paused == paused && seedCriteriaEqual(d.SeedCriteria, seedCriteria) {
		return nil
	}
	d.Paused = paused
	d.SeedCriteria = seedCriteria

	out, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("torrent: marshal descriptor for %s: %w", id, err)
	}
	if err := fsops.AtomicWrite(sidecarFileName(stateDir, id), bytes.NewReader(out), 0o640); err != nil {
		return fmt.Errorf("torrent: persist descriptor for %s: %w", id, err)
	}
	return nil
}

// seedCriteriaEqual is a shallow, pointer-aware comparison good enough to
// decide whether [updateDescriptorState] needs to write: it compares the
// rendered JSON rather than field by field, since commonv1alpha1.SeedCriteria
// carries a *resource.Quantity that has no cheap equality of its own.
func seedCriteriaEqual(a, b *commonv1alpha1.SeedCriteria) bool {
	if a == nil || b == nil {
		return a == b
	}
	aj, errA := json.Marshal(a)
	bj, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(aj) == string(bj)
}

// loadDescriptors reads every persisted descriptor under stateDir. A single
// corrupt or unreadable pair is logged by the caller and skipped rather than
// failing the whole re-attach: one damaged sidecar must not strand every
// other transfer's re-attach behind it. stateDir not existing yet (a brand
// new engine, nothing ever persisted) is not an error and returns no
// descriptors.
func loadDescriptors(stateDir string) ([]loadedDescriptor, []error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("torrent: read state dir %s: %w", stateDir, err)}
	}

	var (
		out  []loadedDescriptor
		errs []error
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")

		body, err := os.ReadFile(sidecarFileName(stateDir, id))
		if err != nil {
			errs = append(errs, fmt.Errorf("torrent: read descriptor for %s: %w", id, err))
			continue
		}
		var d descriptor
		if err := json.Unmarshal(body, &d); err != nil {
			errs = append(errs, fmt.Errorf("torrent: decode descriptor for %s: %w", id, err))
			continue
		}

		var payload []byte
		if b, err := os.ReadFile(torrentFileName(stateDir, id)); err == nil {
			payload = b
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("torrent: read metainfo for %s: %w", id, err))
			continue
		}

		out = append(out, loadedDescriptor{ID: id, Desc: d, Payload: payload})
	}
	return out, errs
}

// addRequest renders l back into the [download.AddRequest] [saveDescriptor]
// was called with. Paused always comes from the descriptor's current value,
// not from whatever the original Add used -- a transfer paused after it was
// added must come back paused, not resume itself the moment the engine
// restarts.
func (l loadedDescriptor) addRequest() download.AddRequest {
	req := download.AddRequest{
		Name:             l.Desc.Name,
		Category:         l.Desc.Category,
		Payload:          l.Payload,
		Magnet:           l.Desc.Magnet,
		ExpectedInfoHash: l.Desc.ExpectedInfoHash,
		Priority:         l.Desc.Priority,
		Paused:           l.Desc.Paused,
		SeedCriteria:     l.Desc.SeedCriteria,
		WantFile:         l.Desc.Selection.selector(),
		AddedAt:          l.Desc.AddedAt,
	}
	return req
}
