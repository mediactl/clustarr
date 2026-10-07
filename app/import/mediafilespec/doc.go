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

// Package mediafilespec is importarr's one render of MediaFileSpec under
// k8s.ManagerImportarrWorker ([Apply], [ReassertFrozen]) and the rename of
// one library file to the path catalogarr proposes ([RenameFile]). The
// rescan worker and the rename controller both write through it, so
// neither can become a second, narrower apply that releases what the
// other sends; it links no worker package, so the manager's rename
// controller does not link the rescan (spec §4.3 I2).
package mediafilespec
