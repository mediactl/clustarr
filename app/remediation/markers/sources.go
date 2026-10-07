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

package markers

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
	"github.com/mediactl/clustarr/pkg/segments"
)

// Sources: S20 (a worker's clustarr-markers answer or deferral) and S21 (a
// clustarr-segments v2 record), §3.3, §4.9.
func (a *Adapter) Sources(ctrl.Manager) ([]remediation.Source, error) {
	return []remediation.Source{
		{Name: "S20/clustarr-markers", Raw: recordsource.New(a.bus, events.BucketMarkers, remediation.FileKey)},
		{Name: "S21/clustarr-segments", Raw: recordsource.New(a.bus, events.BucketSegments, remediation.FileKey,
			recordsource.WithDecoder[remediation.Key](segments.WakeRef))},
	}, nil
}
