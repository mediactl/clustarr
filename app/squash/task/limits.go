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
	"maps"
	"slices"
	"strings"
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

// EncoderLimits is the value under EncoderLimitsKey: each reporter's limits
// and when it measured them. A reporter is a pool pod, "<node>/<pod>"
// (Reporter), so two pods of one class on one node keep their own reports;
// an entry keyed by a bare node is a worker that predates pod keys.
type EncoderLimits struct {
	Nodes map[string]NodeLimits `json:"nodes"`
}

// NodeLimits is one reporter's measured device limits, the tier it
// measured it encodes with, and whether its device could be used at all
// (spec §4): Healthy absent, from a worker that predates health, reads as
// healthy.
type NodeLimits struct {
	transcode.Limits
	Tier       transcode.Tier `json:"tier,omitempty"`
	MeasuredAt time.Time      `json:"measuredAt"`
	Healthy    *bool          `json:"healthy,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// Reporter is the key a pool pod publishes under: its node and its pod, or
// the node alone when the pod's name is unknown.
func Reporter(node, pod string) string {
	if pod == "" {
		return node
	}
	return node + "/" + pod
}

// nodeOf is the node a reporter key names.
func nodeOf(reporter string) string {
	node, _, _ := strings.Cut(reporter, "/")
	return node
}

// NodeHealth is one node's last report of its device.
type NodeHealth struct {
	Healthy bool
	Error   string
}

// MaxHealthMessage is the longest Error published, in bytes: the
// TranscodeProfile status field that shows it is capped at 256.
const MaxHealthMessage = 256

// PublishEncoderLimits merges reporter's limits l, measured at now, into
// class's key with compare-and-swap, dropping any entry older than
// EncoderLimitsFresh, so concurrent workers of one class never overwrite
// each other's reports.
func PublishEncoderLimits(ctx context.Context, kv events.KV, class, reporter string, l transcode.Limits, now time.Time) error {
	return PublishEncoderHealth(ctx, kv, class, reporter, l, nil, now)
}

// PublishEncoderHealth is PublishEncoderLimits with the device's health: a
// nil unhealthy is a healthy device, anything else the reason this
// reporter's pod cannot use it, which the controller reads
// (ReadEncoderHealth) to send the class no work while every report is one.
func PublishEncoderHealth(ctx context.Context, kv events.KV, class, reporter string, l transcode.Limits, unhealthy error, now time.Time) error {
	return PublishMeasurement(ctx, kv, class, reporter, transcode.Measurement{Limits: l}, unhealthy, now)
}

// PublishMeasurement is PublishEncoderHealth with the tier the pod measured
// it encodes with, which the controller plans with (ReadEncoderTier).
func PublishMeasurement(ctx context.Context, kv events.KV, class, node string, m transcode.Measurement, unhealthy error, now time.Time) error {
	l := m.Limits
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
		next := EncoderLimits{Nodes: map[string]NodeLimits{node: {Limits: mine, Tier: m.Tier, MeasuredAt: now, Healthy: &healthy, Error: msg}}}
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

// tightest is the formats both a and b decode (narrowestDecoders): a job
// may land on any node of the class.
func tightest(a, b transcode.Limits) transcode.Limits {
	return transcode.Limits{NVDEC: narrowestDecoders(a.NVDEC, b.NVDEC)}
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

// ReadEncoderLimitsByNode is class's fresh entries, by node: a node's pods'
// limits merged to the tightest.
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
	for r, nl := range cur.Nodes {
		if now.Sub(nl.MeasuredAt) >= EncoderLimitsFresh {
			continue
		}
		if prev, ok := out[nodeOf(r)]; ok {
			out[nodeOf(r)] = tightest(prev, nl.Limits)
		} else {
			out[nodeOf(r)] = nl.Limits
		}
	}
	return out, nil
}

// ReadEncoderHealth is each node's health for class, from the reports its
// pods published within EncoderLimitsFresh of now: healthy while any of
// them is, else the first unhealthy report's error (by reporter, so the
// choice is stable). No entry is no report, which the controller reads as
// "not known to be unhealthy".
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
	reporters := slices.Sorted(maps.Keys(cur.Nodes))
	for _, r := range reporters {
		nl := cur.Nodes[r]
		if now.Sub(nl.MeasuredAt) >= EncoderLimitsFresh {
			continue
		}
		h := NodeHealth{Healthy: nl.Healthy == nil || *nl.Healthy, Error: nl.Error}
		if h.Healthy {
			h.Error = ""
		}
		if prev, ok := out[nodeOf(r)]; ok && (prev.Healthy || !h.Healthy) {
			continue // a healthy report, or the first unhealthy one, stands
		}
		out[nodeOf(r)] = h
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ReadEncoderTier is the tier class's pods measured they encode with: the
// one every fresh healthy report names, or "" when none names one or they
// disagree, and the controller plans the class's default tier.
func ReadEncoderTier(ctx context.Context, kv events.KV, class string, now time.Time) (transcode.Tier, error) {
	key := EncoderLimitsKey(class)
	entry, err := kv.Get(ctx, key)
	if errors.Is(err, events.ErrKeyNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("task: read %s: %w", key, err)
	}
	var cur EncoderLimits
	if err := json.Unmarshal(entry.Value, &cur); err != nil {
		return "", fmt.Errorf("task: decode %s: %w", key, err)
	}
	var tier transcode.Tier
	for _, nl := range cur.Nodes {
		if now.Sub(nl.MeasuredAt) >= EncoderLimitsFresh || (nl.Healthy != nil && !*nl.Healthy) || nl.Tier == "" {
			continue
		}
		if tier != "" && tier != nl.Tier {
			return "", nil
		}
		tier = nl.Tier
	}
	return tier, nil
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
