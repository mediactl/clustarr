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

// No RBAC marker lives here: this package declares the owned sets, runs the
// ladder and publishes its events, and writes nothing to the apiserver. The
// writer is app/indexer/status/statuspatch, which carries the grants (ADR-0019
// A6.3: the index agent links this package, so it must hold no Kubernetes
// writer).
package status
