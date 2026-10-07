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

// Package subtitlestore is the clustarr-subtitles instantiation of
// pkg/records (loop spec §6.6): the loop requests and withdraws through it,
// the caption domain answers through it, and nothing else opens the bucket.
// Each language of a file has its own key, events.RecordSubKey(fileUID,
// langKey), so concurrent languages never contend.
package subtitlestore
