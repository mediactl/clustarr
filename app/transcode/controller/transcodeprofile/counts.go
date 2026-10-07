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

package transcodeprofile

import (
	"context"
	"fmt"
	"sort"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/app/transcode/task"
	busevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// counts is a profile's status counts (§2.13's TranscodeProfile row).
type counts struct{ pending, running, matching, waiting int32 }

// counts reads them: pendingJobs from the Pending, Planned and Queued files
// of the profile, runningJobs from Running and Swapping (through
// remediation.mediafile.transcodeProfile), waitingFiles from the ledger's
// backlog, and matchingFiles from every file whose block names the profile
// plus that backlog.
func (r *Reconciler) counts(ctx context.Context, profile string) (counts, error) {
	n := func(value string) (int32, error) {
		var l catalogv1alpha1.MediaFileList
		if err := r.Client.List(ctx, &l, client.MatchingFields{mfindex.TranscodeProfile: value}, client.UnsafeDisableDeepCopy); err != nil {
			return 0, fmt.Errorf("transcodeprofile: count %s: %w", value, err)
		}
		return int32(len(l.Items)), nil //nolint:gosec // a library's file count fits
	}
	var c counts
	for _, p := range []catalogv1alpha1.TranscodePhase{
		catalogv1alpha1.TranscodePhasePending, catalogv1alpha1.TranscodePhasePlanned,
		catalogv1alpha1.TranscodePhaseQueued,
	} {
		v, err := n(profile + "/" + string(p))
		if err != nil {
			return c, err
		}
		c.pending += v
	}
	for _, p := range []catalogv1alpha1.TranscodePhase{catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping} {
		v, err := n(profile + "/" + string(p))
		if err != nil {
			return c, err
		}
		c.running += v
	}
	all, err := n(profile)
	if err != nil {
		return c, err
	}
	c.waiting = int32(r.Ledger.Backlog(profile)) //nolint:gosec // a library's file count fits
	c.matching = all + c.waiting
	return c, nil
}

// maxEncoderLimits is status.encoderLimits' MaxItems.
const maxEncoderLimits = 16

// encoderLimits is status.encoderLimits: every GPU class's fresh per-node
// entries (task.ReadEncoderLimitsByNode), sorted by class then node and
// capped. An unreadable bucket, or none, shows none rather than failing the
// pass.
func encoderLimits(ctx context.Context, kv busevents.KV, now time.Time) []transcodev1alpha1.EncoderLimit {
	if kv == nil {
		return nil
	}
	var out []transcodev1alpha1.EncoderLimit
	for _, class := range []transcodev1alpha1.Hardware{transcodev1alpha1.HardwareIntel, transcodev1alpha1.HardwareNVIDIA} {
		nodes, err := task.ReadEncoderLimitsByNode(ctx, kv, string(class), now)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "transcodeprofile: cannot read the encoder limits", "class", class, "error", err)
			continue
		}
		health, err := task.ReadEncoderHealth(ctx, kv, string(class), now)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "transcodeprofile: cannot read the devices' health", "class", class, "error", err)
		}
		names := make([]string, 0, len(nodes))
		for n := range nodes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			l := transcodev1alpha1.EncoderLimit{Class: class, Node: n, NVDEC: nodes[n].NVDEC.Decodable()}
			if h, ok := health[n]; ok {
				l.Healthy, l.Message = new(h.Healthy), h.Error
			}
			out = append(out, l)
		}
	}
	if len(out) > maxEncoderLimits {
		out = out[:maxEncoderLimits]
	}
	return out
}
