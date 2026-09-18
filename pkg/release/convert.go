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

package release

import commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"

// ApplyTo copies the fields this package parses onto ri, leaving every
// indexer-sourced field (GUID, DownloadURL, Seeders, ...) untouched.
func (p *ParsedRelease) ApplyTo(ri *commonv1.ReleaseInfo) {
	ri.Quality = p.Quality
	ri.Revision = p.Revision
	ri.ReleaseGroup = p.Group
	ri.Edition = p.Edition
	ri.Languages = p.Languages
	ri.ReleaseType = p.ReleaseType
	if len(p.IDs) > 0 {
		if ri.IDs == nil {
			ri.IDs = make(map[string]string, len(p.IDs))
		}
		for k, v := range p.IDs {
			ri.IDs[k] = v
		}
	}
}
