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

// Package iptv holds Live TV's pure rules (spec 2026-10-07 §4): the M3U
// parser (m3u), xTeVe's filters (filter), the XMLTV parser and guide builder
// (xmltv) and the HDHomeRun surface (hdhr). The tuner and the ui share them,
// so what the ui counts and previews is what Plex gets. Nothing here touches
// a cluster.
package iptv
