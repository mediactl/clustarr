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

package rootfolder

import (
	"fmt"
	"os"

	"github.com/mediactl/clustarr/pkg/fsops"
)

// checkPath is the real, os-backed filesystem probe: the path must exist, be
// a directory, and accept a temp-file write (existence alone is not enough --
// a directory owned by another uid with no write bit exists and is not
// accessible). It never creates or removes anything except its own probe
// file.
func checkPath(path string) (accessible bool, freeBytes, totalBytes int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, 0, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return false, 0, 0, fmt.Errorf("%s is not a directory", path)
	}

	probe, err := os.CreateTemp(path, ".clustarr-rootfolder-check-*")
	if err != nil {
		return false, 0, 0, fmt.Errorf("%s is not writable: %w", path, err)
	}
	name := probe.Name()
	_ = probe.Close()
	if rmErr := os.Remove(name); rmErr != nil {
		return false, 0, 0, fmt.Errorf("remove probe file: %w", rmErr)
	}

	usage, err := fsops.DiskUsage(path)
	if err != nil {
		return true, 0, 0, fmt.Errorf("disk usage %s: %w", path, err)
	}
	return true, usage.Free, usage.Total, nil
}
