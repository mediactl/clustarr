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

// Package overlayplan decides a Movie's or Series' rating overlay: the
// winning OverlayProfile ([Winner], [Selects]), the badges it draws
// ([Badges]), the digest of its inputs ([InputsDigest], [ProfileHash],
// [RenderVersion]), the [Plan] that combines them, and the RenderOverlay task
// ([Publish]). The OverlayProfile controller (manager) and the renderer
// (app/catalog/worker/artwork, agent) share it. It decodes no image, reads
// nothing from the apiserver and carries no RBAC markers (design 2026-10-06
// §4.3 C2).
package overlayplan
