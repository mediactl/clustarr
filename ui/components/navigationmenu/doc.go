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

// Package navigationmenu is Clustarr's own shadcn navigation-menu (the base
// style's navigation-menu.tsx over Base UI's NavigationMenu), written the
// way shadcn-templ's vendored components are because its registry has no
// navigation-menu: one templ part per tsx export, the data-slot names and
// class strings verbatim, data-tui-navigation-menu-* attributes for the
// script (navigationmenu.js, packed into the component bundle by
// `shadcn-templ bundle`), and Base UI's public contract for state
// (data-popup-open, data-open/closed, data-starting-style/ending-style,
// data-activation-direction, data-side/align, the --popup-* and
// --positioner-* variables).
package navigationmenu
