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

package remediation

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
)

// RegisterIndexes registers every index the loop's passes, sources and
// actuators read, once, from app/remediation/manager.Register (§3.16): the
// loop's own (mfindex: UID and Item, the one MediaFile index by item that
// replaced nine) and the ones its domain functions read by their unchanged
// names (mediafile.RegisterIndexes: the TranscodeJob, AudioGraft and
// SubtitleRequest refs and the two RootFolder naming indexes). It registers
// no ".spec.seriesRef": the Series controller does (series.RegisterIndexes),
// and the loop only reads it.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := mfindex.Register(ctx, idx); err != nil {
		return err
	}
	return mediafile.RegisterIndexes(ctx, idx)
}
