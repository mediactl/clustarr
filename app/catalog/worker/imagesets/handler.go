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

// Package imagesets is the metadata agent's image-set worker
// (docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md):
// the catalog-imageset durable's handler asks the registry's image-set
// providers (MediUX, through its cache) for one Movie's or Series' set and
// answers into clustarr-image-sets by compare-and-swap. It reads no
// Kubernetes object: the task carries the ids and the series' shape, which
// the remediation loop built from its cache.
package imagesets

import (
	"context"
	"errors"
	"fmt"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogimagesets "github.com/mediactl/clustarr/app/catalog/imagesets"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/mediuxsets"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// errNoProvider is a gateway whose registry has no image-set provider: the
// mediux provider was disabled or lost its token after the loop asked. The
// task is acked without an answer, so the record stays requested and the
// loop asks again after its request timeout.
var errNoProvider = errors.New("no image-set provider is configured")

// deferAfter is the longest limit a task waits out in its in-flight slot;
// a longer one defers the record and acks, and the remediation loop
// publishes the task again at the reset (ADR-0019 §5.6).
const deferAfter = 5 * time.Minute

// Handler is the catalog-imageset durable's handler.
type Handler struct {
	Providers  []metadata.ImageSetProvider
	Clock      func() time.Time
	Answers    *catalogimagesets.Answers // writes as this pod (NewAnswers' writer)
	MaxDeliver uint64                    // the consumer's; the last delivery answers failed
}

// Handle implements events.Handler.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var task schema.ImageSetTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable image-set task", err)
	}
	if task.Item.UID == "" || task.Seq == 0 {
		return events.Discard("an image-set task with no item or sequence", fmt.Errorf("item=%v seq=%d", task.Item, task.Seq))
	}
	if sup, err := h.Answers.Superseded(ctx, task); err != nil {
		return err
	} else if sup {
		return nil
	}
	now := h.now()
	q := metadata.ImageSetQuery{
		Kind: commonv1.MediaKind(task.Inputs.Kind), TMDBID: task.Inputs.TMDBID, TVDBID: task.Inputs.TVDBID,
		Seasons: task.Inputs.Seasons, Episodes: mediuxsets.EpisodesOf(task.Inputs.Episodes), SetID: task.Inputs.SetID,
	}
	set, err := h.ask(ctx, q)
	var rl *metadata.RateLimitedError
	switch {
	case errors.As(err, &rl) && rl.RetryAfter > deferAfter:
		// Deferred, or superseded or answered meanwhile: either way ack.
		// The record's DeferredUntil wakes the loop, which republishes.
		if _, derr := h.Answers.Defer(ctx, task, now.Add(rl.RetryAfter)); derr != nil {
			return events.Retry(rl.RetryAfter, errors.Join(err, derr))
		}
		return nil
	case errors.As(err, &rl):
		if h.MaxDeliver > 0 && m.Attempt() >= h.MaxDeliver {
			// The last delivery answers failed and transient, never leaving
			// the request to the dead-letter stream.
			_, aerr := h.Answers.Answer(ctx, task, schema.ImageSetAnswer{CheckedAt: now}, err.Error())
			return aerr
		}
		return events.Retry(rl.RetryAfter, err)
	case errors.Is(err, errNoProvider):
		return nil // the registry, not the item: the record stays requested and the loop asks again
	}
	ans := schema.ImageSetAnswer{CheckedAt: now}
	failure := ""
	switch {
	case err == nil:
		ans.Provider, ans.SetID, ans.Title, ans.Creator, ans.URL, ans.Reason = set.Provider, set.SetID, set.Title, set.Creator, set.URL, set.Reason
		if !set.UpdatedAt.IsZero() {
			t := set.UpdatedAt.UTC()
			ans.UpdatedAt = &t
		}
		for _, i := range set.Images {
			ans.Images = append(ans.Images, schema.ImageSetImage{Type: i.Type, Season: i.Season, Episode: i.Episode, URL: i.URL})
		}
		logging.FromContext(ctx).InfoContext(ctx, "image set chosen", "item", task.Item.Name, "kind", task.Inputs.Kind,
			"set", set.SetID, "title", set.Title, "creator", set.Creator, "reason", set.Reason, "images", len(set.Images))
	case errors.Is(err, metadata.ErrNotFound):
		ans.NotFound = true
		if len(h.Providers) > 0 {
			ans.Provider = h.Providers[0].Name()
		}
	default:
		logging.FromContext(ctx).WarnContext(ctx, "image set lookup failed", "item", task.Item.Name, "kind", task.Inputs.Kind, "error", err)
		failure = err.Error()
	}
	_, err = h.Answers.Answer(ctx, task, ans, failure)
	return err
}

// ask takes the first provider's answer; NotFound from every one is
// NotFound, and any other failure is the error.
func (h *Handler) ask(ctx context.Context, q metadata.ImageSetQuery) (*metadata.ImageSet, error) {
	if len(h.Providers) == 0 {
		return nil, errNoProvider
	}
	var last error
	for _, p := range h.Providers {
		set, err := p.ImageSet(ctx, q)
		if err == nil {
			return set, nil
		}
		last = err
		if !errors.Is(err, metadata.ErrNotFound) {
			return nil, err
		}
	}
	return nil, last
}

func (h *Handler) now() time.Time {
	if h.Clock != nil {
		return h.Clock()
	}
	return time.Now()
}
