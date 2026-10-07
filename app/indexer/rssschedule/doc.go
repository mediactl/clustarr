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

// Package rssschedule is the RSS poll chain's schedule: when an Indexer's
// next poll is due, the per-slot Msg-ID that collapses two publishes of one
// slot onto one delivery, and the scheduled publish itself. Since ADR-0019
// A6.3 the Indexer controller publishes every poll (P91); the package links
// no release index, so the manager links no SQLite or Postgres (spec §4.3
// X1).
package rssschedule
