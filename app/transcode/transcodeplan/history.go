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

package transcodeplan

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// HistoryEvent is one clustarr.evt.transcode.job.<action>.<mfUID> event a
// transition owes, published after the apply lands with Msg-Id
// <mfUID>/transcode/<seq>/<action> (loop spec §5.16, §4.10).
type HistoryEvent struct {
	Action string
	Seq    int64
	Reason string
}

// Terminal is a dispatch that just became terminal after running: the R9
// transcode metrics are observed once, on the apply that makes it so
// (§5.16; the TranscodeJob controller's observeFinished, re-typed).
type Terminal struct {
	Class      string
	Succeeded  bool
	Wall       time.Duration
	Height     int32
	Runtime    time.Duration
	SizeRatio  float64 // output/source; 0 when unknown
	Resolution string
}

// terminalOf is the observation for b, whose dispatch just finished, read
// from the draft's probe of the source.
func terminalOf(b *catalogv1alpha1.TranscodeState, st *catalogv1alpha1.MediaFileStatus) *Terminal {
	if b.StartedAt == nil || b.FinishedAt == nil {
		return nil
	}
	t := &Terminal{
		Class:     string(b.Class),
		Succeeded: b.Phase == catalogv1alpha1.TranscodePhaseSwapping || b.Phase == catalogv1alpha1.TranscodePhaseSucceeded,
		Wall:      max(b.FinishedAt.Sub(b.StartedAt.Time), 0),
	}
	if t.Class == "" && b.Plan != nil {
		t.Class = string(HardwareForEncoder(b.Plan.Encoder))
	}
	if mi := st.MediaInfo; mi != nil {
		t.Height, t.Runtime = mi.Height, time.Duration(mi.RuntimeMillis)*time.Millisecond
	}
	if r := b.Result; r != nil && r.OutputToSourcePercent > 0 {
		t.SizeRatio = float64(r.OutputToSourcePercent) / 100
	}
	t.Resolution = resolutionClass(t.Height)
	return t
}

// resolutionClass is the bounded resolution label, sd/hd/uhd, "unknown"
// with no probe.
func resolutionClass(height int32) string {
	switch {
	case height <= 0:
		return "unknown"
	case height <= 576:
		return "sd"
	case height <= 1080:
		return "hd"
	default:
		return "uhd"
	}
}
