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

package grabplan

import (
	"context"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// CountParses counts the planner's release parses until restore is called.
func CountParses() (count func() int, restore func()) {
	saved := parseForItemState
	n := 0
	parseForItemState = func(ctx context.Context, kind commonv1.MediaKind, originalTag string, rel commonv1.ReleaseInfo) (*release.ParsedRelease, error) {
		n++
		return saved(ctx, kind, originalTag, rel)
	}
	return func() int { return n }, func() { parseForItemState = saved }
}
