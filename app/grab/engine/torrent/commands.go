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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Transfers is the torrent engine's engine.Transfers: its embedded client
// and its descriptors, which carry each transfer's journal (ADR-0019 §6.7).
// The descriptors are read once at start and kept in memory beside the
// files they are written to.
type Transfers struct {
	Engine     *Engine
	HTTPClient *http.Client
	Resolver   IndexerResolver
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu     sync.Mutex
	loaded bool
	desc   map[string]descriptor // by client id
}

var _ engine.Transfers = (*Transfers)(nil)

func (t *Transfers) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// load reads every descriptor once.
func (t *Transfers) load(ctx context.Context) {
	if t.loaded {
		return
	}
	t.loaded = true
	t.desc = map[string]descriptor{}
	ds, errs := loadDescriptors(t.Engine.StateDir)
	for _, err := range errs {
		logging.FromContext(ctx).WarnContext(ctx, "torrent: a descriptor could not be read", "error", err)
	}
	for _, d := range ds {
		t.desc[d.ID] = d.Desc
	}
}

// List implements engine.Transfers: every transfer the client holds, with
// its descriptor's journal; seed counters and a moved content root are
// persisted as they are observed (at most once a minute per transfer).
func (t *Transfers) List(ctx context.Context) ([]engine.Held, error) {
	items, err := t.Engine.Client.List(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.load(ctx)
	out := make([]engine.Held, 0, len(items))
	for _, it := range items {
		d, ok := t.desc[it.ID]
		if ok {
			t.observe(ctx, it, d)
		}
		out = append(out, engine.Held{Name: d.Name, Journal: d.Journal, Item: it, Found: true})
	}
	return out, nil
}

// observe persists what a transfer reports that its descriptor keeps: the
// seed counters (nextSeedRecord's rate) and a content root a publish moved.
func (t *Transfers) observe(ctx context.Context, it download.Item, d descriptor) {
	seed, due := nextSeedRecord(d.Seed, it, t.now())
	moved := it.ContentRoot != "" && it.ContentRoot != d.ContentRoot
	if !due && !moved {
		return
	}
	if due {
		d.Seed = &seed
	}
	if moved {
		d.ContentRoot = it.ContentRoot
	}
	if err := saveDescriptor(t.Engine.StateDir, it.ID, nil, d); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "torrent: persist descriptor failed", "id", it.ID, "error", err)
		return
	}
	t.desc[it.ID] = d
}

// Apply implements engine.Transfers.
func (t *Transfers) Apply(ctx context.Context, cmd schema.EngineCommand, cur *engine.Held) (engine.Held, error) {
	switch cmd.Desired {
	case schema.EngineDesiredAbsent:
		return t.remove(ctx, cmd, cur)
	case schema.EngineDesiredPresent:
		if cur == nil || !cur.Found {
			return t.add(ctx, cmd)
		}
		return t.update(ctx, cmd, *cur)
	}
	return engine.Held{}, fmt.Errorf("torrent: unknown desired state %q", cmd.Desired)
}

// add resolves the payload and adds the transfer, journalled.
func (t *Transfers) add(ctx context.Context, cmd schema.EngineCommand) (engine.Held, error) {
	if cmd.Source == nil {
		return engine.Held{}, fmt.Errorf("%w: the command carries no source", engine.ErrPayloadUnavailable)
	}
	res, err := resolveSource(ctx, t.HTTPClient, t.Resolver, cmd.Owner.Namespace, *cmd.Source)
	if err != nil {
		return engine.Held{}, err
	}
	if res.ExpectedInfoHash == "" {
		res.ExpectedInfoHash = cmd.ExpectedInfoHash
	}
	sel := selectionFrom(cmd.Selection)
	req := download.AddRequest{
		Name:             cmd.Entry.ID,
		Magnet:           res.Magnet,
		Payload:          res.Payload,
		ExpectedInfoHash: res.ExpectedInfoHash,
		Category:         category(cmd),
		Paused:           cmd.Paused,
		Priority:         cmd.Priority,
		SeedCriteria:     cmd.SeedCriteria,
		WantFile:         sel.selector(),
		Imported:         cmd.Imported,
	}
	id, err := t.Engine.Client.Add(ctx, req)
	if err != nil {
		return engine.Held{}, err
	}
	item, err := t.Engine.Client.Get(ctx, id)
	if err != nil {
		return engine.Held{}, fmt.Errorf("torrent: get %s after add: %w", id, err)
	}
	d := descriptor{
		Name:             cmd.Entry.ID,
		Category:         req.Category,
		Magnet:           res.Magnet,
		ExpectedInfoHash: res.ExpectedInfoHash,
		Priority:         cmd.Priority,
		Paused:           cmd.Paused,
		SeedCriteria:     cmd.SeedCriteria,
		Selection:        sel,
		AddedAt:          item.AddedAt,
		Journal:          engine.NextJournal(engine.Journal{}, cmd, t.now()),
	}
	if err := saveDescriptor(t.Engine.StateDir, id, res.Payload, d); err != nil {
		return engine.Held{}, fmt.Errorf("torrent: persist descriptor for %s: %w", id, err)
	}
	t.mu.Lock()
	t.load(ctx)
	t.desc[id] = d
	t.mu.Unlock()
	return engine.Held{Name: d.Name, Journal: d.Journal, Item: item, Found: true}, nil
}

// update brings a held transfer to the command's state: pause, priority,
// seed criteria and the import; a pre-journal transfer is adopted.
func (t *Transfers) update(ctx context.Context, cmd schema.EngineCommand, cur engine.Held) (engine.Held, error) {
	c := t.Engine.Client
	id := cur.Item.ID
	switch {
	case cmd.Paused && cur.Item.Status != download.StatusPaused:
		if err := c.Pause(ctx, id); err != nil {
			return engine.Held{}, fmt.Errorf("torrent: pause %s: %w", id, err)
		}
	case !cmd.Paused && cur.Item.Status == download.StatusPaused:
		if err := c.Resume(ctx, id); err != nil {
			return engine.Held{}, fmt.Errorf("torrent: resume %s: %w", id, err)
		}
	}
	if err := c.SetPriority(ctx, id, cmd.Priority); err != nil && !errors.Is(err, download.ErrNotFound) {
		return engine.Held{}, fmt.Errorf("torrent: set priority %s: %w", id, err)
	}
	if cmd.SeedCriteria != nil {
		if err := c.SetSeedCriteria(ctx, id, *cmd.SeedCriteria); err != nil && !errors.Is(err, download.ErrNotFound) {
			return engine.Held{}, fmt.Errorf("torrent: set seed criteria %s: %w", id, err)
		}
	}
	if cmd.Imported {
		if err := c.MarkImported(ctx, id); err != nil && !errors.Is(err, download.ErrNotFound) {
			return engine.Held{}, fmt.Errorf("torrent: mark imported %s: %w", id, err)
		}
	}
	t.mu.Lock()
	t.load(ctx)
	d := t.desc[id]
	if d.Name == "" {
		d.Name = cmd.Entry.ID
	}
	d.Paused, d.Priority, d.SeedCriteria = cmd.Paused, cmd.Priority, cmd.SeedCriteria
	d.Journal = engine.NextJournal(d.Journal, cmd, t.now())
	err := saveDescriptor(t.Engine.StateDir, id, nil, d)
	if err == nil {
		t.desc[id] = d
	}
	t.mu.Unlock()
	if err != nil {
		return engine.Held{}, err
	}
	item, err := c.Get(ctx, id)
	if err != nil {
		return engine.Held{}, fmt.Errorf("torrent: get %s: %w", id, err)
	}
	return engine.Held{Name: d.Name, Journal: d.Journal, Item: item, Found: true}, nil
}

// remove removes a transfer, its data when asked, and its descriptor.
func (t *Transfers) remove(ctx context.Context, cmd schema.EngineCommand, cur *engine.Held) (engine.Held, error) {
	var prev engine.Journal
	var item download.Item
	name := cmd.Entry.ID
	if cur != nil {
		prev, item, name = cur.Journal, cur.Item, cur.Name
		if err := t.RemoveByID(ctx, cur.Item.ID, cmd.RemoveData); err != nil && !errors.Is(err, download.ErrNotFound) {
			return engine.Held{}, err
		}
	}
	return engine.Held{Name: name, Journal: engine.NextJournal(prev, cmd, t.now()), Item: item, Found: false}, nil
}

// RemoveByID implements engine.Transfers.
func (t *Transfers) RemoveByID(ctx context.Context, id string, removeData bool) error {
	err := t.Engine.Client.Remove(ctx, id, removeData)
	if err != nil && !errors.Is(err, download.ErrNotFound) {
		return fmt.Errorf("torrent: remove %s: %w", id, err)
	}
	if derr := removeDescriptor(t.Engine.StateDir, id); derr != nil {
		logging.FromContext(ctx).WarnContext(ctx, "torrent: remove descriptor failed", "id", id, "error", derr)
	}
	t.mu.Lock()
	if t.desc != nil {
		delete(t.desc, id)
	}
	t.mu.Unlock()
	return err
}

// category is the command's category, else the owner kind's name.
func category(cmd schema.EngineCommand) string {
	if cmd.Category != "" {
		return cmd.Category
	}
	return strings.ToLower(cmd.Owner.Kind)
}
