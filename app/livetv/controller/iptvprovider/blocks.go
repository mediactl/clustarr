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

package iptvprovider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/blocks"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// LabelLeaseProvider marks a block's Lease with its provider's name, so the
// controller maps a Lease change to its provider. cluster-plex creates the
// Leases (Live TV spec §6.1); the controller only reads them.
const LabelLeaseProvider = "clustarr.io/iptv-provider"

// BlockGuidePath is a block's XEPG guide under its path.
const BlockGuidePath = "/xmltv.xml"

// LeaseName is the Lease cluster-plex holds while it registers a block.
func LeaseName(deviceID string) string { return "livetv-" + strings.ToLower(deviceID) }

// plan is a provider's blocks for this pass: the ones published in status
// and given to the tuner. When a limit or a rule across providers forbids
// the next blocks, the last good ones are kept and reason says why.
type plan struct {
	blocks          []clustarrv1.BlockStatus
	numbers         map[string][]string // start -> active numbers
	reason, message string
	// lineup is the stored lineup plan while spec.lineup is set; nil when
	// none is stored (or spec.lineup is unset).
	lineup *livetvstore.LineupPlan
	// categoryChannels counts the active channels by status.lineup.category.
	categoryChannels map[string]int32
}

// lineupEnabled reports whether spec.lineup numbers and categorises the
// provider's channels, rather than showing a dry run.
func lineupEnabled(p *clustarrv1.IPTVProvider) bool {
	return p.Spec.Lineup != nil && p.Spec.Lineup.Enabled
}

// categoryOf is the lineup category the planner put ch in; "" is none.
func categoryOf(ch *clustarrv1.IPTVChannel) string {
	if ch.Status.Lineup == nil {
		return ""
	}
	return ch.Status.Lineup.Category
}

// categoryNumbers is the active channels' numbers by their lineup category,
// each list unique and sorted; a channel in no category is in none.
func categoryNumbers(channels []clustarrv1.IPTVChannel) map[string][]string {
	out := map[string][]string{}
	for i := range channels {
		ch := &channels[i]
		if cat := categoryOf(ch); ch.Spec.Active && cat != "" {
			out[cat] = append(out[cat], ch.Spec.Number)
		}
	}
	for cat, ns := range out {
		slices.SortFunc(ns, xmltv.CompareNumbers)
		out[cat] = slices.Compact(ns)
	}
	return out
}

// countCategories counts the active channels in each lineup category.
func countCategories(channels []clustarrv1.IPTVChannel) map[string]int32 {
	out := map[string]int32{}
	for i := range channels {
		if cat := categoryOf(&channels[i]); channels[i].Spec.Active && cat != "" {
			out[cat]++
		}
	}
	return out
}

// readLineup is the provider's stored lineup plan while spec.lineup is set:
// nil when it is unset, when no reader is wired, or when none is stored yet.
func (r *Reconciler) readLineup(ctx context.Context, p *clustarrv1.IPTVProvider) (*livetvstore.LineupPlan, error) {
	if p.Spec.Lineup == nil || r.PlanReader == nil {
		return nil, nil
	}
	lp, ok, err := r.PlanReader(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Name})
	if err != nil || !ok {
		return nil, err
	}
	return lp, nil
}

// categoryBlocks is a block per lineup category holding an active channel
// (the custom guides design §5.5): its start the category's base+1, its
// numbers the category's, in start order. A category the plan refuses as
// TooMany has none, whatever its channels' status still says, and so has a
// category the plan does not list (a channel's status not yet replanned).
func categoryBlocks(lp *livetvstore.LineupPlan, channels []clustarrv1.IPTVChannel) (next []blocks.Block, categories map[string]string) {
	byCat := categoryNumbers(channels)
	categories = map[string]string{}
	for _, c := range lp.Categories {
		numbers := byCat[c.Name]
		if c.TooMany || len(numbers) == 0 {
			continue
		}
		start := strconv.Itoa(c.Base + 1)
		if _, dup := categories[start]; dup {
			continue // bases are unique (CEL); a second is no block
		}
		categories[start] = c.Name
		next = append(next, blocks.Block{Start: start, Numbers: numbers})
	}
	slices.SortFunc(next, func(a, b blocks.Block) int { return xmltv.CompareNumbers(a.Start, b.Start) })
	return next, categories
}

// unapplied counts the channels that do not carry lp yet: a planned channel
// missing, inactive, or off its planned number or category, and an active
// channel still in a category the plan blocks (not refused as TooMany) that
// the plan no longer places there.
func unapplied(lp *livetvstore.LineupPlan, channels []clustarrv1.IPTVChannel) int {
	blocked := map[string]bool{}
	for _, c := range lp.Categories {
		blocked[c.Name] = !c.TooMany
	}
	byKey := make(map[string]*clustarrv1.IPTVChannel, len(channels))
	for i := range channels {
		byKey[channels[i].Spec.Key] = &channels[i]
	}
	n := 0
	planned := make(map[string]bool, len(lp.Channels))
	for _, a := range lp.Channels {
		planned[a.Key] = true
		ch := byKey[a.Key]
		if ch == nil || !ch.Spec.Active || ch.Spec.Number != a.Number || categoryOf(ch) != a.Category {
			n++
		}
	}
	for i := range channels {
		ch := &channels[i]
		if ch.Spec.Active && blocked[categoryOf(ch)] && !planned[ch.Spec.Key] {
			n++
		}
	}
	return n
}

// bounds is the lowest and highest number of next's blocks.
func bounds(next []blocks.Block) (first, last string) {
	for _, b := range next {
		for _, n := range b.Numbers {
			if first == "" || xmltv.CompareNumbers(n, first) < 0 {
				first = n
			}
			if last == "" || xmltv.CompareNumbers(n, last) > 0 {
				last = n
			}
		}
	}
	return first, last
}

// older reports whether a was created before b: the older provider keeps a
// disputed span or device ID.
func older(a, b *clustarrv1.IPTVProvider) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
}

// activeNumbers is the provider's active channel numbers, unique and sorted.
// A duplicate counts once; the IPTVChannel reconciler reports it.
func activeNumbers(channels []clustarrv1.IPTVChannel) []string {
	var out []string
	for i := range channels {
		if channels[i].Spec.Active {
			out = append(out, channels[i].Spec.Number)
		}
	}
	slices.SortFunc(out, xmltv.CompareNumbers)
	return slices.Compact(out)
}

// lineupIDOf is a block's cloud lineup id, from its lineup URI.
func lineupIDOf(uri string) string {
	return (&clustarrv1.PlexGuide{Lineup: uri}).LineupID()
}

// pairedStations is the active channels on Plex in the tuner's snapshot,
// by number: the stations their blocks' channel maps carry. Only a pairing
// with the cloud lineup counts (a station of another lineup is none the cloud
// DVR lists); an empty Lineup is a status from before several lineups, when
// only the cloud lineup existed.
func pairedStations(channels []clustarrv1.IPTVChannel, snap tuner.Snapshot, cloud string) map[string]plexguide.Station {
	out := map[string]plexguide.Station{}
	for i := range channels {
		ch := &channels[i]
		st, ok := snap.Channels[ch.Name]
		if !ch.Spec.Active || !ok || st.Plex == nil || !st.Plex.Match.OnPlex() || (st.Plex.Lineup != "" && st.Plex.Lineup != cloud) {
			continue
		}
		if _, dup := out[ch.Spec.Number]; !dup {
			out[ch.Spec.Number] = st.Plex.Station
		}
	}
	return out
}

// span is the numbers a provider's published blocks cover.
func span(bs []clustarrv1.BlockStatus) (first, last string, ok bool) {
	if len(bs) == 0 || bs[0].First == "" || bs[len(bs)-1].Last == "" {
		return "", "", false
	}
	return bs[0].First, bs[len(bs)-1].Last, true
}

// overlaps reports whether [a1, a2] and [b1, b2] share a number.
func overlaps(a1, a2, b1, b2 string) bool {
	return xmltv.CompareNumbers(a1, b2) <= 0 && xmltv.CompareNumbers(b1, a2) <= 0
}

// planBlocks renders p's next blocks (Live TV spec §3.1, §5.0, §6), with
// the stored lineup plan they follow while spec.lineup is set.
func (r *Reconciler) planBlocks(ctx context.Context, p *clustarrv1.IPTVProvider, channels []clustarrv1.IPTVChannel) (plan, error) {
	lp, err := r.readLineup(ctx, p)
	if err != nil {
		return plan{}, err
	}
	pl, err := r.packBlocks(ctx, p, channels, lp)
	if err != nil {
		return plan{}, err
	}
	pl.lineup, pl.categoryChannels = lp, countCategories(channels)
	return pl, nil
}

// packBlocks renders p's next blocks: a block per lineup category while
// spec.lineup is enabled and its plan applied (the custom guides design
// §5.5), else packed by number.
func (r *Reconciler) packBlocks(ctx context.Context, p *clustarrv1.IPTVProvider, channels []clustarrv1.IPTVChannel, lp *livetvstore.LineupPlan) (plan, error) {
	keep := func(reason, message string) plan {
		kept := slices.Clone(p.Status.Blocks)
		for i := range kept {
			r.address(&kept[i])
		}
		return plan{blocks: kept, numbers: keptNumbers(p.Status.Blocks, channels), reason: reason, message: message}
	}

	var all clustarrv1.IPTVProviderList
	if err := r.Client.List(ctx, &all); err != nil {
		return plan{}, err
	}
	var others []*clustarrv1.IPTVProvider
	for i := range all.Items {
		o := &all.Items[i]
		if o.Namespace != p.Namespace || o.Name != p.Name {
			others = append(others, o)
		}
	}

	// With spec.lineup enabled and its plan applied, a block per category
	// holding an active channel (the custom guides design §5.5). Until the
	// channels carry the plan, the published blocks stand: a plan that
	// failed (a rule that does not compile), one the lineup controller has
	// not applied to every channel yet, or none stored, or only a dry run's,
	// while any block is published must not take a DVR off Plex. That covers
	// the one-edit flip (plexGuide.enabled false, lineup.enabled true): until
	// the lineup controller's first enabled plan lands, packing by number
	// would publish a fresh XMLTV block of every channel at its DIRECTV or
	// parked number, and cluster-plex would put it in the cloud DVR's place.
	// Only a provider with nothing published packs by number meanwhile.
	var catOf map[string]string // start -> category
	var next []blocks.Block
	if lineupEnabled(p) {
		switch {
		case lp != nil && lp.Error != "":
			return keep(clustarrv1.ReasonInvalidLineup, "spec.lineup is invalid: "+lp.Error), nil
		case lp == nil || lp.DryRun:
			if len(p.Status.Blocks) > 0 {
				return keep(clustarrv1.ReasonLineupPending, "waiting for the lineup controller's first enabled plan: the published blocks are kept"), nil
			}
		default:
			// The lineup controller stores the plan, then applies each
			// channel's number and status in turn: a block built meanwhile
			// would hold old or half-rewritten numbers (one number in two
			// categories' blocks), and every such render would move a block's
			// lineupHash, a channel-map save and a guide rebuild in Plex.
			if n := unapplied(lp, channels); n > 0 {
				return keep(clustarrv1.ReasonLineupPending, fmt.Sprintf("waiting for the lineup controller to apply its plan: %d channels do not carry their planned number and category yet", n)), nil
			}
			next, catOf = categoryBlocks(lp, channels)
			if len(next) > blocks.MaxBlocks {
				return keep(clustarrv1.ReasonTooManyChannels, fmt.Sprintf("%d lineup categories hold active channels, more than %d blocks", len(next), blocks.MaxBlocks)), nil
			}
		}
	}

	numbers := activeNumbers(channels)
	lim := blocks.Limits{MaxChannels: int(p.Spec.MaxChannelsOrDefault()), MaxBytes: blocks.DefaultMaxBytes}
	// With spec.plexGuide.enabled the blocks hold the channels paired with
	// the Plex lineup alone, each a device whose DVR's guide is that lineup;
	// a channel off Plex is in no block (the Plex native guide design §5.4).
	// A lineup's categories take the blocks over from Plex's.
	plexOn := catOf == nil && p.Spec.PlexGuide != nil && p.Spec.PlexGuide.Enabled
	var lineup string
	if plexOn {
		snap, ok := r.Tuner.Snapshot(types.NamespacedName{Namespace: p.Namespace, Name: p.Name})
		switch {
		case !ok || !snap.Plex.Loaded:
			return keep(clustarrv1.ReasonLineupPending, "waiting for the Plex lineup "+p.Spec.PlexGuide.LineupID()), nil
		case !snap.PlaylistFetched:
			// A lineup read from its cache ahead of the playlist pairs no
			// channel (a render places none before the first playlist): the
			// published blocks stay, or every Plex DVR would go.
			return keep(clustarrv1.ReasonLineupPending, "waiting for the playlist to pair the channels with the Plex lineup"), nil
		}
		paired := pairedStations(channels, snap, p.Spec.PlexGuide.LineupID())
		numbers = numbers[:0:0]
		for n := range paired {
			numbers = append(numbers, n)
		}
		slices.SortFunc(numbers, xmltv.CompareNumbers)
		lim.Cost = func(n string) int { s := paired[n]; return blocks.PlexCost(n, s.ID, s.VCN) }
		lineup = plexguide.LineupURI(snap.Plex.LineupID, snap.Plex.Title)
	}
	prev := make([]string, 0, len(p.Status.Blocks))
	byStart := map[string]clustarrv1.BlockStatus{}
	byCategory := map[string]clustarrv1.BlockStatus{}
	for _, b := range p.Status.Blocks {
		if b.Category != "" {
			// A category's block keeps its device across a new base; its
			// start is no boundary of a packing by number.
			byCategory[b.Category] = b
			continue
		}
		// A switch to or from Plex's guide, or to another Plex lineup, packs
		// afresh: the other blocks' starts are no boundaries of these.
		if (b.Lineup != "") != plexOn || (plexOn && lineupIDOf(b.Lineup) != p.Spec.PlexGuide.LineupID()) {
			continue
		}
		prev = append(prev, b.Start)
		byStart[b.Start] = b
	}
	if catOf == nil {
		var err error
		next, err = blocks.Next(prev, numbers, lim)
		if errors.Is(err, blocks.ErrTooManyBlocks) {
			return keep(clustarrv1.ReasonTooManyChannels, fmt.Sprintf("%d active channels need more than %d blocks", len(numbers), blocks.MaxBlocks)), nil
		}
		if err != nil {
			return plan{}, err
		}
	}

	// The rule across providers: a span another provider has published
	// stands, so the provider whose edit would overlap it is refused, older
	// or younger. Only when the published spans already overlap (a status
	// lost and re-packed) does age decide: the younger is refused.
	if len(next) > 0 {
		first, last := bounds(next)
		pf, pl, published := span(p.Status.Blocks)
		for _, o := range others {
			of, ol, ok := span(o.Status.Blocks)
			if !ok || !overlaps(first, last, of, ol) {
				continue
			}
			if already := published && overlaps(pf, pl, of, ol); !already || older(o, p) {
				return keep(clustarrv1.ReasonNumberRangeOverlap,
					fmt.Sprintf("numbers %s-%s overlap provider %s/%s's %s-%s", first, last, o.Namespace, o.Name, of, ol)), nil
			}
		}
	}

	out := plan{numbers: map[string][]string{}}
	friendly := p.Spec.Device.FriendlyNameOrDefault(p.Name)
	for i, nb := range next {
		bs := clustarrv1.BlockStatus{Start: nb.Start, Category: catOf[nb.Start]}
		if bs.Category != "" {
			bs.DeviceID = byCategory[bs.Category].DeviceID
		} else if old, ok := byStart[nb.Start]; ok {
			bs.DeviceID = old.DeviceID
		}
		if bs.DeviceID == "" {
			switch {
			case bs.Category != "":
				// The category is the device's identity (the custom guides
				// design §5.5): a renumbering or a new base keeps it.
				bs.DeviceID = hdhr.BlockDeviceID(p.Namespace, p.Name, "cat/"+bs.Category)
			case plexOn:
				// A device of its own, per lineup, so turning Plex's guide on,
				// or naming another lineup, replaces the devices and their DVRs
				// rather than changing a DVR's lineup in place, which PMS has
				// no call for.
				bs.DeviceID = hdhr.BlockDeviceID(p.Namespace, p.Name, "plex/"+p.Spec.PlexGuide.LineupID()+"/"+nb.Start)
			case len(prev) == 0 && i == 0 && p.Spec.Device.ID != "":
				bs.DeviceID = p.Spec.Device.ID
			default:
				bs.DeviceID = hdhr.BlockDeviceID(p.Namespace, p.Name, nb.Start)
			}
		}
		r.address(&bs)
		bs.Channels = int32(len(nb.Numbers))
		bs.First, bs.Last = nb.Numbers[0], nb.Numbers[len(nb.Numbers)-1]
		if lim.Cost != nil {
			bs.MapBytes = int32(blocks.MapBytesWith(nb.Numbers, lim.Cost))
		} else {
			bs.MapBytes = int32(blocks.MapBytes(nb.Numbers))
		}
		bs.FriendlyName = friendly + " " + nb.Start + "+"
		if bs.Category != "" {
			bs.FriendlyName = friendly + " " + bs.Category
		}
		// spec.device.friendlyName (128) and a category (64) can outgrow
		// the status field (160), which the apiserver would refuse.
		bs.FriendlyName = clampRunes(bs.FriendlyName, maxBlockFriendlyName)
		if plexOn {
			bs.Lineup = lineup
		} else {
			bs.GuidePath = BlockGuidePath
		}
		out.blocks = append(out.blocks, bs)
		out.numbers[nb.Start] = nb.Numbers
	}

	for _, o := range others {
		for _, ob := range o.Status.Blocks {
			for _, b := range out.blocks {
				if b.DeviceID == ob.DeviceID && older(o, p) {
					return keep(clustarrv1.ReasonDuplicateDeviceID,
						fmt.Sprintf("device ID %s is provider %s/%s's block %s", b.DeviceID, o.Namespace, o.Name, ob.Start)), nil
				}
			}
		}
	}
	return out, nil
}

// maxBlockFriendlyName is status.blocks[].friendlyName's MaxLength, which
// the apiserver counts in runes.
const maxBlockFriendlyName = 160

// clampRunes cuts s to at most n runes.
func clampRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// address gives b its device path and its URI on the livetv Service: the
// URL Plex is given. With no Address the URI is empty, and cluster-plex
// registers nothing for the block.
func (r *Reconciler) address(b *clustarrv1.BlockStatus) {
	b.Path = tuner.DeviceEndpoint(b.DeviceID)
	b.URI = ""
	if r.Address != "" && b.DeviceID != "" {
		b.URI = "http://" + r.Address + b.Path
	}
}

// keptNumbers places the active channels' numbers in the kept blocks, so
// the tuner keeps serving the last good blocks: a category's block its
// category's channels, the others as blocks.Next would without splitting.
func keptNumbers(bs []clustarrv1.BlockStatus, channels []clustarrv1.IPTVChannel) map[string][]string {
	out := map[string][]string{}
	if len(bs) == 0 {
		return out
	}
	var byCat map[string][]string
	starts := make([]string, 0, len(bs))
	for _, b := range bs {
		if b.Category != "" {
			if byCat == nil {
				byCat = categoryNumbers(channels)
			}
			out[b.Start] = byCat[b.Category]
			continue
		}
		starts = append(starts, b.Start)
	}
	if len(starts) == 0 {
		return out
	}
	numbers := activeNumbers(channels)
	next, err := blocks.Next(starts, numbers, blocks.Limits{MaxChannels: 1 << 30, MaxBytes: 1 << 30})
	if err != nil {
		return out
	}
	for _, b := range next {
		out[b.Start] = b.Numbers
	}
	return out
}

// owners reads each block's Lease: its holder while the Lease is live.
func (r *Reconciler) owners(ctx context.Context, namespace string, bs []clustarrv1.BlockStatus, now time.Time) error {
	for i := range bs {
		bs[i].Owner = ""
		var l coordinationv1.Lease
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: LeaseName(bs[i].DeviceID)}, &l)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if l.Spec.HolderIdentity == nil || l.Spec.RenewTime == nil || l.Spec.LeaseDurationSeconds == nil {
			continue
		}
		if l.Spec.RenewTime.Add(time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second).After(now) {
			bs[i].Owner = *l.Spec.HolderIdentity
		}
	}
	return nil
}

// tunerBlocks is the plan as the tuner takes it.
func tunerBlocks(pl plan) []tuner.BlockConfig {
	out := make([]tuner.BlockConfig, 0, len(pl.blocks))
	for _, b := range pl.blocks {
		out = append(out, tuner.BlockConfig{
			Start: b.Start, DeviceID: b.DeviceID, FriendlyName: b.FriendlyName, Numbers: pl.numbers[b.Start],
			Lineup: b.Lineup, Category: b.Category,
		})
	}
	return out
}
