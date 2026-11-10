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

package views

import (
	"context"
	"io"

	"github.com/a-h/templ"
)

// joined renders parts in order with one space between them -- the
// whitespace templ writes between sibling nodes of one block, which a
// switch or a case drops -- so a table row or a button group composed from
// a list renders byte for byte as the same cells written out did.
func joined(parts []templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		for i, c := range parts {
			if i > 0 {
				if _, err := io.WriteString(w, " "); err != nil {
					return err
				}
			}
			if err := c.Render(ctx, w); err != nil {
				return err
			}
		}
		return nil
	})
}

// clampPercent keeps a progress bar's width inside [0, 100] even if a caller
// passes Entry's "no percentage" sentinel (-1) or an out-of-range value.
func clampPercent(p int32) int32 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return p
	}
}

// audioTone is an AudioNote's badge colour.
func audioTone(n AudioNote) string {
	if n.Warn {
		return "text-amber-600 dark:text-amber-400"
	}
	return "text-muted-foreground"
}
