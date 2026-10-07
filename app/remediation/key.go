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
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// ControllerName is the loop's controller name: the MediaFile controller's,
// kept (split spec §3.11).
const ControllerName = "mediafile"

// Key is the loop's request: one controller, one queue; Kind picks the path.
type Key struct {
	Kind      KeyKind
	Namespace string
	Name      string
}

// KeyKind is a key's object kind. F4.1 adds the six item kinds (§3.2).
type KeyKind string

// KindMediaFile is the file key's kind.
const KindMediaFile KeyKind = "MediaFile"

// NamespacedName is k's object.
func (k Key) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Namespace: k.Namespace, Name: k.Name}
}

// FileKey is the file key a record's mediaFile ref names; false for a ref
// with no name (a records waker's toKey, §4.9).
func FileKey(ref schema.Ref) (Key, bool) {
	if ref.Name == "" {
		return Key{}, false
	}
	return Key{Kind: KindMediaFile, Namespace: ref.Namespace, Name: ref.Name}, true
}

// Priorities a pass sets on its requeue (§3.11). Watch events are 0; the
// initial list and profile fan-outs are handler.LowPriority.
const (
	PriorityUser  = 100 // an intent annotation, a user's label, an admission grant
	PriorityTimed = -50 // a requeue from a planner's Due
)
