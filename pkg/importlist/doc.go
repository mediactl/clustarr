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

// Package importlist is Clustarr's import-list layer: the shared vocabulary
// (Item, ExternalIDs, the ImportList fetch interface, a name-keyed
// Registry), the plain-Go config union that mirrors
// api/catalog/v1alpha1.ImportListSpec's provider fields, and a TokenStore
// abstraction for providers that hold an OAuth token across reconciles.
//
// Concrete providers live in subpackages (trakt, plex, mdblist, stevenlu,
// imdbcsv, tmdb, custom, arr), each implementing ImportList against one
// remote source. This package has no Kubernetes dependency: it is wired to
// the ImportList CRD by the importarr ImportList controller (Phase G),
// which reads SecretRef/ConfigMapRef, converts CRD types to the Config
// structs here, and turns a fetched []Item into catalog objects.
package importlist
