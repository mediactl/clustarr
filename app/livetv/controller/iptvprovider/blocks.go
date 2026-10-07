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
	"strings"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/blocks"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// LabelLeaseProvider marks a block's Lease with its provider's name, so the
// controller maps a Lease change to its provider. cluster-plex creates the
// Leases (Live TV spec §6.1); the controller only reads them.
const LabelLeaseProvider = "clustarr.io/iptv-provider"

// BlockGuidePath is a block's XEPG guide under its address.
const BlockGuidePath = "/xmltv.xml"

// PortRange is the ports blocks are allocated from (--livetv-port-range).
type PortRange struct{ Low, High int32 }

// LeaseName is the Lease cluster-plex holds while it registers a block.
func LeaseName(deviceID string) string { return "livetv-" + strings.ToLower(deviceID) }

// plan is a provider's blocks for this pass: the ones published in status
// and given to the tuner. When a limit or a rule across providers forbids
// the next blocks, the last good ones are kept and reason says why.
type plan struct {
	blocks          []clustarrv1.BlockStatus
	numbers         map[string][]string // start -> active numbers
	reason, message string
}

// ports is the controller's in-memory port reservations: what it has handed
// out, by provider, so a cache that lags its own last write never hands one
// port out twice (MaxConcurrentReconciles stays 1).
type ports struct {
	mu   sync.Mutex
	held map[int32]types.NamespacedName
}

func (ps *ports) holder(port int32) (types.NamespacedName, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	k, ok := ps.held[port]
	return k, ok
}

// set records exactly the ports key holds now.
func (ps *ports) set(key types.NamespacedName, held []int32) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.held == nil {
		ps.held = map[int32]types.NamespacedName{}
	}
	for port, k := range ps.held {
		if k == key {
			delete(ps.held, port)
		}
	}
	for _, port := range held {
		ps.held[port] = key
	}
}

// older reports whether a was created before b: the older provider keeps a
// disputed span, device ID or port.
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

// errNoFreePort is a port range with no port left.
var errNoFreePort = errors.New("livetv: no free port in --livetv-port-range")

// planBlocks renders p's next blocks (Live TV spec §3.1, §5.0, §6).
func (r *Reconciler) planBlocks(ctx context.Context, p *clustarrv1.IPTVProvider, channels []clustarrv1.IPTVChannel) (plan, error) {
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	keep := func(reason, message string) plan {
		return plan{blocks: slices.Clone(p.Status.Blocks), numbers: keptNumbers(p.Status.Blocks, activeNumbers(channels)), reason: reason, message: message}
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

	numbers := activeNumbers(channels)
	prev := make([]string, 0, len(p.Status.Blocks))
	byStart := map[string]clustarrv1.BlockStatus{}
	for _, b := range p.Status.Blocks {
		prev = append(prev, b.Start)
		byStart[b.Start] = b
	}
	next, err := blocks.Next(prev, numbers, blocks.Limits{MaxChannels: int(p.Spec.MaxChannelsOrDefault()), MaxBytes: blocks.DefaultMaxBytes})
	if errors.Is(err, blocks.ErrTooManyBlocks) {
		return keep(clustarrv1.ReasonTooManyChannels, fmt.Sprintf("%d active channels need more than %d blocks", len(numbers), blocks.MaxBlocks)), nil
	}
	if err != nil {
		return plan{}, err
	}

	// The rules across providers: the provider created later loses.
	if len(next) > 0 {
		first, last := next[0].Numbers[0], next[len(next)-1].Numbers[len(next[len(next)-1].Numbers)-1]
		for _, o := range others {
			if of, ol, ok := span(o.Status.Blocks); ok && overlaps(first, last, of, ol) && older(o, p) {
				return keep(clustarrv1.ReasonNumberRangeOverlap,
					fmt.Sprintf("numbers %s-%s overlap provider %s/%s's %s-%s", first, last, o.Namespace, o.Name, of, ol)), nil
			}
		}
	}

	usedByOthers := func(port int32) bool {
		for _, o := range others {
			for _, b := range o.Status.Blocks {
				if b.Port == port && older(o, p) {
					return true
				}
			}
		}
		if k, ok := r.ports.holder(port); ok && k != key {
			return true
		}
		return false
	}
	taken := map[int32]bool{}
	allocate := func() (int32, error) {
		for port := r.Ports.Low; port <= r.Ports.High; port++ {
			if !taken[port] && !usedByOthers(port) && !heldByAny(others, port) {
				return port, nil
			}
		}
		return 0, errNoFreePort
	}

	out := plan{numbers: map[string][]string{}}
	friendly := p.Spec.Device.FriendlyNameOrDefault(p.Name)
	for i, nb := range next {
		bs := clustarrv1.BlockStatus{Start: nb.Start}
		if old, ok := byStart[nb.Start]; ok {
			bs.DeviceID, bs.Port = old.DeviceID, old.Port
		}
		if bs.DeviceID == "" {
			bs.DeviceID = hdhr.BlockDeviceID(p.Namespace, p.Name, nb.Start)
			if len(prev) == 0 && i == 0 && p.Spec.Device.ID != "" {
				bs.DeviceID = p.Spec.Device.ID
			}
		}
		if bs.Port == 0 || taken[bs.Port] || usedByOthers(bs.Port) {
			port, err := allocate()
			if err != nil {
				return plan{}, err
			}
			bs.Port = port
		}
		taken[bs.Port] = true
		bs.Channels = int32(len(nb.Numbers))
		bs.First, bs.Last = nb.Numbers[0], nb.Numbers[len(nb.Numbers)-1]
		bs.MapBytes = int32(blocks.MapBytes(nb.Numbers))
		bs.FriendlyName = friendly + " " + nb.Start + "+"
		if p.Spec.EPGSourceOrDefault() == clustarrv1.EPGSourceXEPG {
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

// heldByAny reports whether any other provider's status holds port. A
// younger provider's claim does not stop an older one from keeping a port it
// already has, but a new port is never one another provider shows.
func heldByAny(others []*clustarrv1.IPTVProvider, port int32) bool {
	for _, o := range others {
		for _, b := range o.Status.Blocks {
			if b.Port == port {
				return true
			}
		}
	}
	return false
}

// keptNumbers places numbers in the kept blocks, as blocks.Next would
// without splitting, so the tuner keeps serving the last good blocks.
func keptNumbers(bs []clustarrv1.BlockStatus, numbers []string) map[string][]string {
	out := map[string][]string{}
	if len(bs) == 0 {
		return out
	}
	starts := make([]string, len(bs))
	for i, b := range bs {
		starts[i] = b.Start
	}
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
		out = append(out, tuner.BlockConfig{Start: b.Start, DeviceID: b.DeviceID, FriendlyName: b.FriendlyName, Port: b.Port, Numbers: pl.numbers[b.Start]})
	}
	return out
}
