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

package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// EncoderLimitsFresh is how long a node's measured limits count after it
// last published them. A pool worker republishes with every task, and the
// clustarr-progress bucket's own TTL (10 minutes) expires a class no worker
// refreshes at all.
const EncoderLimitsFresh = 10 * time.Minute

// encoderLimitsAttempts bounds PublishEncoderLimits' compare-and-swap loop:
// each lost race is another node's publish, and a class has few nodes.
const encoderLimitsAttempts = 5

// EncoderLimitsKey is the clustarr-progress key a class's pool workers
// merge their device limits into.
func EncoderLimitsKey(class string) string {
	return "encoder-limits." + events.KVKeyToken(class)
}

// EncoderLimits is the value under EncoderLimitsKey: each node's limits and
// when it measured them.
type EncoderLimits struct {
	Nodes map[string]NodeLimits `json:"nodes"`
}

// NodeLimits is one node's measured device limits, and whether its device
// could be used at all (spec §4): Healthy absent, from a worker that
// predates health, reads as healthy.
type NodeLimits struct {
	transcode.Limits
	MeasuredAt time.Time `json:"measuredAt"`
	Healthy    *bool     `json:"healthy,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// NodeHealth is one node's last report of its device.
type NodeHealth struct {
	Healthy bool
	Error   string
}

// MaxHealthMessage is the longest Error published, in bytes: the
// TranscodeProfile status field that shows it is capped at 256.
const MaxHealthMessage = 256

// PublishEncoderLimits merges node's limits l, measured at now, into
// class's key with compare-and-swap, dropping any entry older than
// EncoderLimitsFresh, so concurrent workers of one class never overwrite
// each other's nodes.
func PublishEncoderLimits(ctx context.Context, kv events.KV, class, node string, l transcode.Limits, now time.Time) error {
	return PublishEncoderHealth(ctx, kv, class, node, l, nil, now)
}

// PublishEncoderHealth is PublishEncoderLimits with the device's health: a
// nil unhealthy is a healthy device, anything else the reason this node's
// pod cannot use it, which the controller reads (ReadEncoderHealth) to send
// the class no work while every node reports one.
func PublishEncoderHealth(ctx context.Context, kv events.KV, class, node string, l transcode.Limits, unhealthy error, now time.Time) error {
	key := EncoderLimitsKey(class)
	healthy, msg := unhealthy == nil, ""
	if unhealthy != nil {
		msg = clampMessage(unhealthy.Error(), MaxHealthMessage)
	}
	for range encoderLimitsAttempts {
		var (
			cur EncoderLimits
			rev uint64
		)
		entry, err := kv.Get(ctx, key)
		switch {
		case errors.Is(err, events.ErrKeyNotFound):
		case err != nil:
			return fmt.Errorf("task: read %s: %w", key, err)
		default:
			rev = entry.Revision
			if err := json.Unmarshal(entry.Value, &cur); err != nil {
				cur = EncoderLimits{} // unreadable: rebuilt from this node's entry
			}
		}
		mine := l
		if prev, ok := cur.Nodes[node]; ok && now.Sub(prev.MeasuredAt) < EncoderLimitsFresh {
			// A device's limit does not loosen: a profile asking for less
			// learns no limit, which must not erase one another profile
			// measured on this node.
			mine = tightest(prev.Limits, l)
		}
		next := EncoderLimits{Nodes: map[string]NodeLimits{node: {Limits: mine, MeasuredAt: now, Healthy: &healthy, Error: msg}}}
		for n, nl := range cur.Nodes {
			if n != node && now.Sub(nl.MeasuredAt) < EncoderLimitsFresh {
				next.Nodes[n] = nl
			}
		}
		val, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("task: encode %s: %w", key, err)
		}
		if rev == 0 {
			_, err = kv.Create(ctx, key, val)
		} else {
			_, err = kv.Update(ctx, key, val, rev)
		}
		if errors.Is(err, events.ErrKeyExists) || errors.Is(err, events.ErrRevisionMismatch) {
			continue // another node published in between: merge again
		}
		if err != nil {
			return fmt.Errorf("task: write %s: %w", key, err)
		}
		return nil
	}
	return fmt.Errorf("task: write %s: lost %d races", key, encoderLimitsAttempts)
}

// ReadEncoderLimits is class's device limits as the controller plans with
// them: for each limit, the tightest over the nodes that published within
// EncoderLimitsFresh of now, since a job may land on any of them. No entry
// is no limit.
func ReadEncoderLimits(ctx context.Context, kv events.KV, class string, now time.Time) (transcode.Limits, error) {
	nodes, err := ReadEncoderLimitsByNode(ctx, kv, class, now)
	if err != nil {
		return transcode.Limits{}, err
	}
	var out transcode.Limits
	for _, l := range nodes {
		out = tightest(out, l)
	}
	return out, nil
}

// tightest is, per limit, the lower of a's and b's, and the formats both
// decode (narrowestDecoders); a limit only one of them knows is kept.
func tightest(a, b transcode.Limits) transcode.Limits {
	pick := func(x, y *int32) *int32 {
		switch {
		case x == nil:
			return y
		case y == nil || *x <= *y:
			return x
		default:
			return y
		}
	}
	return transcode.Limits{
		MaxBFrames: pick(a.MaxBFrames, b.MaxBFrames), MaxLookahead: pick(a.MaxLookahead, b.MaxLookahead),
		NVDEC: narrowestDecoders(a.NVDEC, b.NVDEC),
	}
}

// narrowestDecoders is, per format, decodable only when every side that
// measured it decodes it -- a job may land on any node of the class -- and
// a format only one side measured keeps that side's answer. Nil is
// unmeasured on both.
func narrowestDecoders(a, b *transcode.Decoders) *transcode.Decoders {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := &transcode.Decoders{Formats: make(map[string]bool, len(a.Formats)+len(b.Formats))}
	for k, ok := range a.Formats {
		out.Formats[k] = ok
	}
	for k, ok := range b.Formats {
		if prev, seen := out.Formats[k]; seen {
			ok = ok && prev
		}
		out.Formats[k] = ok
	}
	return out
}

// ReadEncoderLimitsByNode is class's fresh entries, by node.
func ReadEncoderLimitsByNode(ctx context.Context, kv events.KV, class string, now time.Time) (map[string]transcode.Limits, error) {
	key := EncoderLimitsKey(class)
	entry, err := kv.Get(ctx, key)
	if errors.Is(err, events.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("task: read %s: %w", key, err)
	}
	var cur EncoderLimits
	if err := json.Unmarshal(entry.Value, &cur); err != nil {
		return nil, fmt.Errorf("task: decode %s: %w", key, err)
	}
	out := make(map[string]transcode.Limits, len(cur.Nodes))
	for n, nl := range cur.Nodes {
		if now.Sub(nl.MeasuredAt) < EncoderLimitsFresh {
			out[n] = nl.Limits
		}
	}
	return out, nil
}

// ReadEncoderHealth is each node's last health report for class, among the
// nodes that published within EncoderLimitsFresh of now: no entry is no
// report, which the controller reads as "not known to be unhealthy".
func ReadEncoderHealth(ctx context.Context, kv events.KV, class string, now time.Time) (map[string]NodeHealth, error) {
	key := EncoderLimitsKey(class)
	entry, err := kv.Get(ctx, key)
	if errors.Is(err, events.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("task: read %s: %w", key, err)
	}
	var cur EncoderLimits
	if err := json.Unmarshal(entry.Value, &cur); err != nil {
		return nil, fmt.Errorf("task: decode %s: %w", key, err)
	}
	out := map[string]NodeHealth{}
	for n, nl := range cur.Nodes {
		if now.Sub(nl.MeasuredAt) < EncoderLimitsFresh {
			out[n] = NodeHealth{Healthy: nl.Healthy == nil || *nl.Healthy, Error: nl.Error}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// clampMessage cuts s to at most max bytes on a rune boundary.
func clampMessage(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
