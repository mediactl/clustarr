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

// Package naming is Clustarr's file and folder naming engine: a token
// grammar and per-media-kind path builder that mirrors the *arr family's
// (Radarr/Sonarr/Lidarr/Readarr) naming tokens, with presets for the four
// media-server dialects (Jellyfin, Plex, Emby, Kodi) the catalog.clustarr.io
// RootFolder type exposes, plus a general-purpose path sanitiser.
//
// pkg/naming is pure Go: it does no I/O and takes no context.Context,
// because every exported function here is a pure string transform. It does
// not import any Kubernetes-generated package except
// api/common/v1alpha1 (aliased commonv1), the shared vocabulary of
// MediaKind, Quality, Source, Modifier, Revision, HdrFormat and MediaInfo.
// Phase C's catalogarr controller is responsible for converting the
// generated catalogv1alpha1.NamingSpec into a naming.Config, and for
// casting its enum values into this package's Dialect / ColonReplacement /
// MultiEpisodeStyle types -- values chosen to match 1:1 so that cast is a
// trivial string conversion.
package naming
