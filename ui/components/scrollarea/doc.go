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

// Package scrollarea is Clustarr's own shadcn scroll-area (the base style's
// scroll-area.tsx over Base UI's ScrollArea), written the way shadcn-templ's
// vendored components are because its registry has no scroll-area: the
// Root, Viewport, Content, Scrollbar, Thumb and Corner parts with the
// data-slot names and class strings verbatim, data-tui-scroll-area-*
// attributes for the script (scrollarea.js, packed into the component
// bundle by `shadcn-templ bundle`), and Base UI's public contract for
// state (data-hovering, data-scrolling, data-has-overflow-x/y,
// data-overflow-*-start/end, the --scroll-area-* variables).
package scrollarea
