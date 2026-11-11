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

package version

import "strings"

// ImageTag is the tag of an image reference, or "" when it names none: the
// value a pod the manager builds carries as app.kubernetes.io/version, the
// build the pod runs, as the chart's pods carry their image's tag
// (structured-logging design §2). A digest is not a tag (its colon is no
// label value), and a registry's port is not one either.
func ImageTag(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon <= slash {
		return ""
	}
	return ref[colon+1:]
}
