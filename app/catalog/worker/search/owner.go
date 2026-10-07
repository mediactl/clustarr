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

package search

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// ownerRef is a grab owner's schema.ItemRef: the object whose
// status.downloads would hold the grab, and whose BlockScopeOf names its item
// blocks (ADR-0019 §6.1, §6.14). The queue, the blocklist and the current
// file are the manager's item-state inputs now (§6.6): the worker reads
// none of them.
func ownerRef(kind string, o client.Object) schema.ItemRef {
	return schema.ItemRef{Kind: kind, Ref: schema.Ref{
		Namespace: o.GetNamespace(), Name: o.GetName(), UID: string(o.GetUID()),
	}}
}
