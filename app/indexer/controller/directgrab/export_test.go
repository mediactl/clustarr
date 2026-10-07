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

package directgrab

import (
	"context"
	"time"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// CountGrabForTest drives the direct-grab accounting path at now, without a
// Download, a manager or an HTTP server.
func CountGrabForTest(ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer, guid string) {
	now := time.Now()
	_ = countGrabAt(ctx, bus, idx, guid, now, now, logging.FromContext(ctx))
}
