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

// Package replay is the remediation loop's replay actuator (loop spec
// §3.9): a clustarr.io/replay request on a MediaFile, handled by the one
// Replayer every replay-<kind> controller uses, without a MediaFile
// controller of its own -- MediaFile never joins replay.ReplayKinds, which
// would make a second MediaFile watcher. The manager imports it as
// replayactuator.
package replay
