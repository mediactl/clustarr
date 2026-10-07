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

package ffprobeexec

import (
	"context"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// Prober is [Probe] and [ProbeAudio] as a mediainfo.Prober: what the import
// domain's probe worker answers with until pkg/mediainfo/native replaces this
// package (spec 2026-10-06 §4.2.5, §6.9). It holds no state.
type Prober struct{}

var _ mediainfo.Prober = Prober{}

// Probe runs ffprobe on path; see [Probe].
func (Prober) Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	return Probe(ctx, path)
}

// ProbeAudio reads path's first audio stream; see [ProbeAudio].
func (Prober) ProbeAudio(ctx context.Context, path string) (mediainfo.AudioProbe, error) {
	return ProbeAudio(ctx, path)
}
