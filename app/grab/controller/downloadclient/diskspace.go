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

package downloadclient

import "github.com/mediactl/clustarr/pkg/fsops"

// DefaultMinFreeBytes is the floor [Reconciler.MinFreeBytes] uses when left
// at zero. DownloadClientSpec has no minFreeBytes field of its own (verified
// against downloadclient_types.go, unlike RootFolderSpec), so this is
// invented the same way catalogarr/controller/rootfolder invented its
// recheckInterval: a cheap, documented default rather than a real signal.
// 1Gi is comfortably above what a stalled or near-full engine volume needs to
// finish an in-flight write, and comfortably below the smallest sane media
// library volume.
const DefaultMinFreeBytes int64 = 1 << 30 // 1Gi

// diskUsageFunc is the filesystem probe [Reconciler] calls for DiskSpaceOK.
// Production wires fsops.DiskUsage; tests inject a fake so they never need a
// real /data mount, mirroring catalogarr/controller/rootfolder's CheckPath.
type diskUsageFunc func(path string) (fsops.Usage, error)
