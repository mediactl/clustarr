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

package torrent

import (
	"path/filepath"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// torrentEngineDirs is where a torrent engine keeps content: publish is
// spec.torrent.publishDir, else <DataDir>/torrents as always; scratch is
// the working area (--scratch-dir, which the controller sets to
// spec.torrent.scratch.path or the scratch mount) only when
// spec.torrent.scratch is set, since the flag always carries a default.
func torrentEngineDirs(o Options, t *downloadv1alpha1.TorrentSpec) (publish, scratch string) {
	publish = filepath.Join(o.DataDir, "torrents")
	if t != nil && t.PublishDir != "" {
		publish = t.PublishDir
	}
	if t != nil && t.Scratch != nil {
		scratch = o.ScratchDir
	}
	return publish, scratch
}
