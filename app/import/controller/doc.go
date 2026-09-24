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

// Package controller will hold importarr's reconcilers: ImportList,
// ImportExclusion, LibraryScan and the RootFolder schedule (amendment
// §A1.6). They run leader-elected on the `importarr` Deployment, selected by
// [github.com/mediactl/clustarr/app/import.RoleController].
//
// Empty for M0/Phase A; the reconcilers land in M1.
package controller
