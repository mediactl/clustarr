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

// Package build constructs the subtitle provider clients the fetch worker
// searches with -- OpenSubtitles.com, Gestdown, SubDL, SubSource and the
// embedded reader -- and caches each until its spec or Secret changes,
// sharing the OpenSubtitles login through the throttle KV (TokenCache).
// Only the caption agent links it; the manager validates providers
// through app/caption/providerset alone (spec §4.3 P1, §13 OD9).
//
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
package build
