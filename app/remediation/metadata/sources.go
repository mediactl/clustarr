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

package metadata

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// Sources implements remediation.Watcher: S25, a gateway answer in
// clustarr-item-metadata wakes its item (§8.1). Artist and Author records
// are their reconcilers' (A5.1 step 4), not the loop's.
func (s *Stage) Sources(ctrl.Manager) ([]remediation.Source, error) {
	src := recordsource.NewItems(s.o.Bus, events.BucketItemMetadata, func(item schema.ItemRef, _ []byte) []remediation.Key {
		k := remediation.KeyKind(item.Kind)
		if !applies(k) || item.Name == "" {
			return nil
		}
		return []remediation.Key{{Kind: k, Namespace: item.Namespace, Name: item.Name}}
	})
	return []remediation.Source{{Name: "S25/" + events.BucketItemMetadata, Raw: src}}, nil
}
