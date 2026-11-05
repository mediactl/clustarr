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

// Package drminfo reads what the kernel's DRM driver says about a render
// node through the fdinfo of an open handle to it (Documentation/gpu/
// drm-usage-stats.rst): the driver's name and how many video engines the
// device has, the capacity admission shares between Plex's streams and
// clustarr's encodes.
package drminfo

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Info is what one DRM handle's fdinfo says of its device.
type Info struct {
	// Driver is the drm-driver line ("i915", "xe", "amdgpu"); "" when the
	// handle is no DRM client at all.
	Driver string
	// VideoEngines is drm-engine-capacity-video, the number of video
	// (media) engines the device exposes; 1 when the driver names a video
	// engine without a capacity line, which the stats format defines as
	// one; 0 when the handle is no DRM client or the driver reports no
	// video engine.
	VideoEngines int
}

// Parse reads fdinfo's key: value lines.
func Parse(r io.Reader) Info {
	var in Info
	video, capacity := false, 0
	s := bufio.NewScanner(r)
	for s.Scan() {
		k, v, ok := strings.Cut(s.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "drm-driver":
			in.Driver = v
		case "drm-engine-video":
			video = true
		case "drm-engine-capacity-video":
			capacity, _ = strconv.Atoi(v)
		}
	}
	if in.Driver == "" || !video {
		return in
	}
	in.VideoEngines = max(capacity, 1)
	return in
}

// Read opens node and parses the fdinfo of the handle; the handle is
// closed before it returns.
func Read(node string) (Info, error) {
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		return Info{}, fmt.Errorf("drminfo: open %s: %w", node, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", f.Fd()))
	if err != nil {
		return Info{}, fmt.Errorf("drminfo: %s: %w", node, err)
	}
	defer func() { _ = fi.Close() }()
	return Parse(fi), nil
}

// VideoEngines is Read(node).VideoEngines, 0 for a node that cannot be
// read or says nothing: unknown, which admission treats as the one engine
// older devices have.
func VideoEngines(node string) int {
	if node == "" {
		return 0
	}
	in, err := Read(node)
	if err != nil {
		return 0
	}
	return in.VideoEngines
}
