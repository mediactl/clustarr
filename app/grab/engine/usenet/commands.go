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

package usenet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Transfers is the usenet engine's engine.Transfers: its embedded client,
// whose scratch manifest carries each job's journal (download.Journaled,
// ADR-0019 §6.7).
type Transfers struct {
	Client   download.Client
	Resolver *Resolver
	// ResolveTimeout overrides DefaultResolveTimeout.
	ResolveTimeout time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

var _ engine.Transfers = (*Transfers)(nil)

func (t *Transfers) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Transfers) journaled() (download.Journaled, error) {
	j, ok := t.Client.(download.Journaled)
	if !ok {
		return nil, errors.New("usenet: the client keeps no journal")
	}
	return j, nil
}

// List implements engine.Transfers.
func (t *Transfers) List(ctx context.Context) ([]engine.Held, error) {
	items, err := t.Client.List(ctx)
	if err != nil {
		return nil, err
	}
	jc, err := t.journaled()
	if err != nil {
		return nil, err
	}
	out := make([]engine.Held, 0, len(items))
	for _, it := range items {
		raw, err := jc.Journal(ctx, it.ID)
		if err != nil && !errors.Is(err, download.ErrNotFound) {
			return nil, err
		}
		out = append(out, engine.Held{Name: it.Name, Journal: engine.UnmarshalJournal(raw), Item: it, Found: true})
	}
	return out, nil
}

// Apply implements engine.Transfers.
func (t *Transfers) Apply(ctx context.Context, cmd schema.EngineCommand, cur *engine.Held) (engine.Held, error) {
	switch cmd.Desired {
	case schema.EngineDesiredAbsent:
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
	case schema.EngineDesiredPresent:
		id := ""
		var prev engine.Journal
		if cur != nil && cur.Found {
			id, prev = cur.Item.ID, cur.Journal
		} else {
			added, err := t.add(ctx, cmd)
			if err != nil {
				return engine.Held{}, err
			}
			id = added
		}
		if err := t.update(ctx, cmd, id, prev); err != nil {
			return engine.Held{}, err
		}
		item, err := t.Client.Get(ctx, id)
		if err != nil {
			return engine.Held{}, fmt.Errorf("usenet: get %s: %w", id, err)
		}
		next := engine.NextJournal(prev, cmd, t.now())
		return engine.Held{Name: item.Name, Journal: next, Item: item, Found: true}, nil
	}
	return engine.Held{}, fmt.Errorf("usenet: unknown desired state %q", cmd.Desired)
}

// add resolves the payload and adds the job; the client dedupes on the name
// (CLAUDE.md: an .nzb is not byte-stable across fetches), so a job already
// running under the entry's id is adopted, not fetched again.
func (t *Transfers) add(ctx context.Context, cmd schema.EngineCommand) (string, error) {
	if byName, ok := t.Client.(download.ByName); ok {
		if it, err := byName.FindByName(ctx, cmd.Entry.ID); err == nil {
			return it.ID, nil
		}
	}
	if cmd.Source == nil {
		return "", fmt.Errorf("%w: the command carries no source", engine.ErrPayloadUnavailable)
	}
	timeout := t.ResolveTimeout
	if timeout <= 0 {
		timeout = DefaultResolveTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	payload, err := t.Resolver.Resolve(rctx, cmd.Owner.Namespace, *cmd.Source)
	cancel()
	if err != nil {
		return "", err
	}
	cat := cmd.Category
	if cat == "" {
		cat = strings.ToLower(cmd.Owner.Kind)
	}
	return t.Client.Add(ctx, download.AddRequest{
		Name:     cmd.Entry.ID,
		Payload:  payload,
		Category: cat,
		Paused:   cmd.Paused,
		Priority: cmd.Priority,
		Imported: cmd.Imported,
	})
}

// update brings the job to the command's state -- priority, pause, a
// resume of a health hold (a new healthOverride nonce: Pause, which lifts
// the hold, then Resume), the import -- and journals it.
func (t *Transfers) update(ctx context.Context, cmd schema.EngineCommand, id string, prev engine.Journal) error {
	if err := t.Client.SetPriority(ctx, id, cmd.Priority); err != nil {
		return err
	}
	item, err := t.Client.Get(ctx, id)
	if err != nil {
		return err
	}
	if cmd.HealthOverride != "" && cmd.HealthOverride != prev.HealthOverride && item.HealthPaused {
		if err := t.Client.Pause(ctx, id); err != nil {
			return err
		}
		item.Status, item.HealthPaused = download.StatusPaused, false
	}
	paused := item.Status == download.StatusPaused
	switch {
	case cmd.Paused && !paused:
		if err := t.Client.Pause(ctx, id); err != nil {
			return err
		}
	case !cmd.Paused && paused && !item.HealthPaused:
		if err := t.Client.Resume(ctx, id); err != nil {
			return err
		}
	}
	if cmd.Imported {
		if err := t.Client.MarkImported(ctx, id); err != nil && !errors.Is(err, download.ErrNotFound) {
			return err
		}
	}
	jc, err := t.journaled()
	if err != nil {
		return err
	}
	body, err := engine.MarshalJournal(engine.NextJournal(prev, cmd, t.now()))
	if err != nil {
		return err
	}
	return jc.SetJournal(ctx, id, body)
}

// RemoveByID implements engine.Transfers: the client's Remove discards the
// job's manifest and scratch directory whatever removeData says (an
// unidentified job's bytes on the data volume are kept).
func (t *Transfers) RemoveByID(ctx context.Context, id string, removeData bool) error {
	err := t.Client.Remove(ctx, id, removeData)
	if err != nil && !errors.Is(err, download.ErrNotFound) {
		logging.FromContext(ctx).WarnContext(ctx, "usenet: remove failed", "id", id, "error", err)
		return fmt.Errorf("usenet: remove %s: %w", id, err)
	}
	return err
}
