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

package mediafile

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// probeState is what a reconcile learns about the file on disk before
// deciding whether to re-run ffprobe. Pure and cluster-free so the
// staleness rule is unit-testable without envtest.
type probeState struct {
	SizeBytes int64
	ModTime   metav1.Time
	Hash      string
	Stale     bool
}

// evaluateProbe hashes the file's current stat against currentHash
// (MediaFile.status.probeHash as last observed). An empty currentHash --
// never probed -- is always stale.
func evaluateProbe(path string, statSize int64, statMod time.Time, currentHash string) probeState {
	h := mediainfo.ProbeHash(path, statSize, statMod)
	return probeState{
		SizeBytes: statSize,
		ModTime:   metav1.NewTime(statMod),
		Hash:      h,
		Stale:     h != currentHash,
	}
}
