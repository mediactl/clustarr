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

package lifecycle

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// ScopeFor is a block's scope by its reason (§6.14): facts about the
// payload itself block it for every item; everything judged against one
// item, or a swarm's state at the time, blocks it for that item only.
func ScopeFor(reason commonv1.DownloadFailureReason) catalogv1alpha1.BlockScope {
	switch reason {
	case commonv1.DownloadFailureEncrypted, commonv1.DownloadFailurePayloadMismatch, commonv1.DownloadFailureMissingArticles:
		return catalogv1alpha1.BlockScopeGlobal
	default:
		// importRejected, stalled, payloadUnavailable, timeout (R21), manual.
		return catalogv1alpha1.BlockScopeItem
	}
}

// scopeString is the release index's scope for a block of the owner: "*"
// for a global block, else schema.BlockScopeOf the owner.
func scopeString(scope catalogv1alpha1.BlockScope, owner schema.ItemRef) string {
	if scope == catalogv1alpha1.BlockScopeGlobal {
		return schema.BlockScopeGlobal
	}
	return schema.BlockScopeOf(owner)
}
