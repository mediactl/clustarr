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

// Package agent is the trailers domain of cmd/agent (ADR-0023): the one
// process that fetches a Movie's or Series' own trailer. It runs in the
// optional trailers image, the only one that holds yt-dlp, and is off
// unless the installer turns it on.
//
// Plex's metadata service has no trailer for about a third of a library
// (talos, 2026-11-17: 203 of 1,273 movies, 390 of 541 shows), while TMDB
// and TheTVDB name one for most -- by its page on YouTube, which Plex
// cannot play. The domain passes over the catalog (Sweeper): for an item
// Plex has no trailer for it asks the metadata gateway which videos are
// named, fetches the pick with yt-dlp, joins its picture and sound in
// process (engine.MuxPair), places the MP4 under the item's root folder at
// .clustarr/trailers/, and records it in the clustarr-trailers bucket. The
// ui's Plex provider answers that file as the item's trailer
// (ui/plex/extras.go), and PMS plays it as a local part.
//
// The domain writes no Kubernetes object and reads Movies, Series and
// RootFolders through the agent's cache. It is one replica: the sweeper is
// the bucket's only writer.
package agent

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;rootfolders,verbs=get;list;watch
