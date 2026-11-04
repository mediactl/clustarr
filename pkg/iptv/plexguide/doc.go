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

// Package plexguide pairs a provider's channels with the stations of a Plex
// cloud (Gracenote) lineup, the guide Plex serves for a cable or satellite
// lineup (the Plex native guide design, 2026-11-03). It decodes the lineup
// as epg.provider.plex.tv answers it and decides each channel's pairing; it
// reads no cluster and fetches nothing.
package plexguide
