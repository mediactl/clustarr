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

// Package overlay is the poster badge model (spec §C.5): Badge, Template
// (DefaultTemplate, TemplateSpec, TemplateHash), FormatScore and the embedded
// rating-source logos. pkg/overlay/render draws it. The manager's
// OverlayProfile controller links this package to resolve and hash templates,
// so it links no golang.org/x/image (spec §4.3 step 1.4, §4.5.1).
package overlay
