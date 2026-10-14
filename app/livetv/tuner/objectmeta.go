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

package tuner

import (
	"strings"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/version"
)

// objectMeta is the metadata of a clustarr-livetv object named name, the
// store's one builder (ADR-0021 §2): its meta-version, its kind (the key's
// first segment: playlist, guide-channels or guide), and who wrote it,
// beside the gzip headers. Every Put sends it whole.
func objectMeta(name, contentType string) events.ObjectMeta {
	kind, _, _ := strings.Cut(name, "/")
	return events.ObjectMeta{
		Headers: map[string]string{"Content-Type": contentType, "Content-Encoding": "gzip"},
		Metadata: map[string]string{
			events.ObjectMetaKeyVersion:   events.LiveTVMetaVersion,
			events.ObjectMetaKeyKind:      kind,
			events.ObjectMetaKeyWrittenBy: "manager@" + version.String(),
		},
	}
}
