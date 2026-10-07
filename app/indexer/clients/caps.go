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

package clients

// MaxCapsItems mirrors the CRD's +kubebuilder:validation:MaxItems=200 on
// status.caps.categories and on each Category.Sub. Exceeding it is an
// apiserver rejection of the whole apply, so the projection truncates rather
// than letting one chatty indexer make its own status unwritable.
const MaxCapsItems = 200
