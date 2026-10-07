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

// Package mediafilespec is import's description of MediaFileSpec under
// k8s.ManagerImportWorker: the frozen fields ([Frozen], [ReassertFrozen]),
// the spec hash a scan observation's basis carries ([SpecHash]), the
// observed-fingerprint hand-over and the missing-file rule a scan and the
// manager's scan applier share. It holds no Kubernetes writer (ADR-0019
// A3.8, A6.1): the writers are specwrite's, which only the manager links.
package mediafilespec
