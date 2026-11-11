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

package lineup

import (
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv/blocks"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/version"
)

// DeleteAbsentAfter is how long a channel the lineup created may be absent
// from the playlist before it is deleted (§5.2: the key is the playlist's,
// stable across token rotations, so an entry back within it finds its own
// channel and a person's edits on it).
const DeleteAbsentAfter = 7 * 24 * time.Hour

// change is what one IPTVChannel is to become.
type change struct {
	// name is the object's; ch the existing object, nil for one to create.
	name string
	ch   *clustarrv1.IPTVChannel
	key  string
	// spec applies number and active (with providerRef and key); a dry
	// run's changes are status alone.
	spec   bool
	number string
	active bool
	// label sends the lineup's origin label: on a channel it creates, and on
	// one whose label already reads lineup -- an apply that omitted a label
	// this manager set would release it, and so remove it.
	label  bool
	status clustarrv1.ChannelLineupStatus
	// remove deletes the channel: the lineup's own, absent DeleteAbsentAfter.
	remove bool
}

// madeByLineup is a channel carrying the lineup's origin label.
func madeByLineup(ch *clustarrv1.IPTVChannel) bool {
	return ch.Labels[clustarrv1.LabelOrigin] == clustarrv1.OriginLineup
}

// channelsByKey indexes a provider's channels by spec.key. Two channels of
// one key (one made by hand under another name) are one too many: the one
// named by names.ChannelObjectName stands, else the first in channels (which
// index.ChannelsOf orders by name), and the other is left as it is.
func channelsByKey(provider string, channels []clustarrv1.IPTVChannel) map[string]*clustarrv1.IPTVChannel {
	canonical := func(ch *clustarrv1.IPTVChannel) bool {
		return ch.Name == names.ChannelObjectName(provider, ch.Spec.Key)
	}
	out := make(map[string]*clustarrv1.IPTVChannel, len(channels))
	for i := range channels {
		ch := &channels[i]
		if prev, ok := out[ch.Spec.Key]; !ok || (!canonical(prev) && canonical(ch)) {
			out[ch.Spec.Key] = ch
		}
	}
	return out
}

// overrides are a person's spec.category and spec.name on the existing
// channels, by key. A spec.name that is the entry's raw or trimmed name is
// no override: the ui's add posts the playlist's raw name as spec.name on
// every channel it makes, which would otherwise keep each one's raw,
// untrimmed name and sort by it. trim is the planner's.
func overrides(byKey map[string]*clustarrv1.IPTVChannel, entries []m3u.Entry, trim func(string) string) map[string]lineup.Override {
	raw := make(map[string]string, len(entries))
	for _, e := range entries {
		raw[e.Key] = e.Name
	}
	out := map[string]lineup.Override{}
	for key, ch := range byKey {
		var ov lineup.Override
		ov.Category = ch.Spec.Category
		if n := ch.Spec.Name; n != "" {
			if r, ok := raw[key]; !ok || (n != r && n != trim(r)) {
				ov.Name = n
			}
		}
		if ov.Category != nil || ov.Name != "" {
			out[key] = ov
		}
	}
	return out
}

// toEntries reduces the tuner's admitted entries to the planner's.
func toEntries(in []m3u.Entry) []lineup.Entry {
	out := make([]lineup.Entry, 0, len(in))
	for _, e := range in {
		out = append(out, lineup.Entry{Key: e.Key, Name: e.Name, Group: e.Group, Logo: e.Logo})
	}
	return out
}

// changes turns plan into one change per channel: every assignment (a
// channel created while enabled), every drop whose channel exists, and,
// while enabled, every channel of the provider whose key is in no entry at
// all. A key both assigned and dropped (two playlist entries of one name
// key) is assigned.
func changes(provider string, plan lineup.Plan, byKey map[string]*clustarrv1.IPTVChannel, enabled bool, now time.Time) []change {
	var out []change
	seen := make(map[string]bool, len(plan.Channels)+len(plan.Dropped))
	for _, a := range plan.Channels {
		if seen[a.Key] {
			continue
		}
		seen[a.Key] = true
		ch := byKey[a.Key]
		st := clustarrv1.ChannelLineupStatus{Name: a.Name, Category: a.Category, Rule: a.Rule, Number: a.Number}
		switch {
		case !enabled && ch == nil:
			// A dry run makes no channel.
		case !enabled:
			out = append(out, change{name: ch.Name, ch: ch, key: a.Key, status: st})
		case ch == nil:
			out = append(out, change{
				name: names.ChannelObjectName(provider, a.Key), key: a.Key, spec: true,
				number: a.Number, active: true, label: true, status: st,
			})
		default:
			out = append(out, change{
				name: ch.Name, ch: ch, key: a.Key, spec: true,
				number: a.Number, active: true, label: madeByLineup(ch), status: st,
			})
		}
	}
	for _, d := range plan.Dropped {
		if seen[d.Key] {
			continue
		}
		seen[d.Key] = true
		ch := byKey[d.Key]
		if ch == nil {
			continue
		}
		c := change{
			name: ch.Name, ch: ch, key: d.Key,
			// A drop has no rule; its reason is the plan object's.
			status: clustarrv1.ChannelLineupStatus{Name: d.Name, DuplicateOf: d.DuplicateOf},
		}
		if enabled {
			c.spec, c.number, c.label = true, ch.Spec.Number, madeByLineup(ch)
		}
		out = append(out, c)
	}
	if !enabled {
		return out
	}
	absent := make([]string, 0, len(byKey))
	for key := range byKey {
		if !seen[key] {
			absent = append(absent, key)
		}
	}
	slices.Sort(absent)
	for _, key := range absent {
		ch := byKey[key]
		since := metav1.NewTime(now.UTC().Truncate(time.Second))
		st := clustarrv1.ChannelLineupStatus{}
		if prev := ch.Status.Lineup; prev != nil {
			st.Name = prev.Name
			if prev.MissingSince != nil {
				since = *prev.MissingSince
			}
		}
		st.MissingSince = &since
		out = append(out, change{
			name: ch.Name, ch: ch, key: key, spec: true, number: ch.Spec.Number,
			label: madeByLineup(ch), status: st,
			remove: madeByLineup(ch) && !now.Before(since.Add(DeleteAbsentAfter)),
		})
	}
	return out
}

// refuseOverBytes refuses a category whose numbers' channel map is over
// blocks.DefaultMaxBytes, cluster-plex's limit for one DVR's map (400
// five-digit numbers are about 30.8 KB), as the planner refuses one over
// its channel count: TooMany, every assignment of it a tooMany drop. The
// planner knows no map bytes; the rule is the DVR's, so it is here. It
// returns each refused category's map bytes.
func refuseOverBytes(plan lineup.Plan) (lineup.Plan, map[string]int) {
	numbers := map[string][]string{}
	for _, a := range plan.Channels {
		numbers[a.Category] = append(numbers[a.Category], a.Number)
	}
	over := map[string]int{}
	for i := range plan.Categories {
		c := &plan.Categories[i]
		if b := blocks.MapBytes(numbers[c.Name]); !c.TooMany && b > blocks.DefaultMaxBytes {
			c.TooMany, over[c.Name] = true, b
		}
	}
	if len(over) == 0 {
		return plan, nil
	}
	kept := make([]lineup.Assignment, 0, len(plan.Channels))
	for _, a := range plan.Channels {
		if _, ok := over[a.Category]; ok {
			plan.Dropped = append(plan.Dropped, lineup.Dropped{Key: a.Key, Name: a.Name, Reason: lineup.ReasonTooMany})
			continue
		}
		kept = append(kept, a)
	}
	plan.Channels = kept
	return plan, over
}

// The bounds of ChannelLineupStatus's fields (their CRD MaxLength).
const (
	maxLineupName     = 256
	maxLineupCategory = 64
	maxLineupRule     = 64
	maxLineupNumber   = 16
	maxLineupKey      = 64
)

// clampStatus fits st to its CRD bounds, each string sanitized and cut to
// at most its MaxLength in runes (as the apiserver counts it, so a valid
// 64-rune category is kept whole): a playlist name is unbounded, and a rule label
// ("<category>[<index>]") can run 4 past a 64-rune category. One refused
// status would hold the provider's category blocks back for good. It is
// applied before st is compared with the stored status, so a clamped value
// reads as unchanged on the next pass.
func clampStatus(st clustarrv1.ChannelLineupStatus) clustarrv1.ChannelLineupStatus {
	fit := func(s string, n int) string { return clampRunes(k8s.SanitizeText(s), n) }
	st.Name = fit(st.Name, maxLineupName)
	st.Category = fit(st.Category, maxLineupCategory)
	st.Rule = fit(st.Rule, maxLineupRule)
	st.Number = fit(st.Number, maxLineupNumber)
	st.DuplicateOf = fit(st.DuplicateOf, maxLineupKey)
	return st
}

// nextDeletion is how long until the first absent channel the lineup made
// is due for deletion; 0 when none waits.
func nextDeletion(cs []change, now time.Time) time.Duration {
	var next time.Duration
	for _, c := range cs {
		if c.remove || c.status.MissingSince == nil || c.ch == nil || !madeByLineup(c.ch) {
			continue
		}
		if d := c.status.MissingSince.Add(DeleteAbsentAfter).Sub(now); d > 0 && (next == 0 || d < next) {
			next = d
		}
	}
	return next
}

// objectMeta is the metadata of the lineup-plan object, the
// clustarr-livetv store's shape (ADR-0021 §2, as the tuner writes its
// objects): its meta-version, its kind (the key's first segment) and who
// wrote it, beside the gzip headers. Every Put sends it whole.
func objectMeta(name string) events.ObjectMeta {
	kind, _, _ := strings.Cut(name, "/")
	return events.ObjectMeta{
		Headers: map[string]string{"Content-Type": "application/json", "Content-Encoding": "gzip"},
		Metadata: map[string]string{
			events.ObjectMetaKeyVersion:   events.LiveTVMetaVersion,
			events.ObjectMetaKeyKind:      kind,
			events.ObjectMetaKeyWrittenBy: "manager@" + version.String(),
		},
	}
}

// clampRunes cuts s to at most n runes.
func clampRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}
