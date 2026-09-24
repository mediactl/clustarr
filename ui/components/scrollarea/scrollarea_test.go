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

package scrollarea_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui/components/scrollarea"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, c.Render(context.Background(), &b))
	return b.String()
}

func with(parent templ.Component, children ...templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return parent.Render(templ.WithChildren(ctx, templ.Join(children...)), w)
	})
}

func text(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	})
}

func tagWith(t *testing.T, body, attr string) string {
	t.Helper()
	i := strings.Index(body, attr)
	require.GreaterOrEqual(t, i, 0, "no element carries %s in\n%s", attr, body)
	start := strings.LastIndex(body[:i], "<")
	end := strings.Index(body[i:], ">")
	require.GreaterOrEqual(t, end, 0)
	return body[start : i+end+1]
}

func requireTag(t *testing.T, body, anchor string, wants ...string) string {
	t.Helper()
	tag := tagWith(t, body, anchor)
	for _, w := range wants {
		require.Contains(t, tag, w, "the element carrying %s", anchor)
	}
	return tag
}

// The parts are shadcn's base scroll-area.tsx over Base UI's ScrollArea:
// Root, Viewport, Content, Scrollbar, Thumb and Corner, the data-slot names
// and class strings verbatim, and data-tui-scroll-area-* for the script,
// which measures the viewport and draws the thumb.
func TestScrollAreaRendersShadcnsPartsOverBaseUIs(t *testing.T) {
	body := render(t, with(scrollarea.ScrollArea(scrollarea.Props{ID: "sa", Class: "h-72 w-48 rounded-md border"}), text("a long list")))

	root := requireTag(t, body, `data-slot="scroll-area"`, `id="sa"`, "data-tui-scroll-area", "relative", "h-72", "w-48")
	require.True(t, strings.HasPrefix(root, "<div"), root)

	viewport := requireTag(t, body, `data-slot="scroll-area-viewport"`, "data-tui-scroll-area-viewport", `tabindex="0"`,
		"size-full", "rounded-[inherit]", "transition-[color,box-shadow]", "outline-none", "focus-visible:ring-[3px]", "focus-visible:ring-ring/50", "focus-visible:outline-1",
		"overflow-auto", "[scrollbar-width:none]", "[&::-webkit-scrollbar]:hidden")
	require.NotEmpty(t, viewport)
	content := requireTag(t, body, `data-slot="scroll-area-content"`, "data-tui-scroll-area-content", "min-w-fit")
	require.NotEmpty(t, content)
	at := strings.Index(body, `data-slot="scroll-area-content"`)
	require.Contains(t, body[at:], "a long list", "the children render inside the content wrapper")
	require.Less(t, strings.Index(body, `data-slot="scroll-area-viewport"`), at, "the content sits inside the viewport")

	require.Equal(t, 1, strings.Count(body, `data-slot="scroll-area-scrollbar"`), "vertical only, by default")
	bar := requireTag(t, body, `data-slot="scroll-area-scrollbar"`, "data-tui-scroll-area-scrollbar", `data-orientation="vertical"`, "data-vertical", " hidden",
		"flex", "touch-none", "p-px", "transition-colors", "select-none",
		"data-horizontal:h-2.5", "data-horizontal:flex-col", "data-horizontal:border-t", "data-horizontal:border-t-transparent",
		"data-vertical:h-full", "data-vertical:w-2.5", "data-vertical:border-l", "data-vertical:border-l-transparent",
		"absolute", "data-vertical:top-0", "data-vertical:right-0", "data-vertical:bottom-(--scroll-area-corner-height)",
		"data-horizontal:bottom-0", "data-horizontal:left-0", "data-horizontal:right-(--scroll-area-corner-width)")
	require.NotContains(t, bar, "data-horizontal ", "the vertical bar is not also horizontal")
	require.NotContains(t, bar, "keep-mounted", "the bar leaves the DOM while nothing overflows, as Base UI's does")
	require.Less(t, strings.Index(body, `data-slot="scroll-area-viewport"`), strings.Index(body, `data-slot="scroll-area-scrollbar"`), "the bar follows the viewport")
	requireTag(t, body, `data-slot="scroll-area-thumb"`, "data-tui-scroll-area-thumb", `data-orientation="vertical"`, "relative", "flex-1", "rounded-full", "bg-border")
	corner := requireTag(t, body, `data-slot="scroll-area-corner"`, "data-tui-scroll-area-corner", " hidden", "absolute", "right-0", "bottom-0",
		"w-(--scroll-area-corner-width)", "h-(--scroll-area-corner-height)")
	require.NotEmpty(t, corner)
}

// Orientation picks the bars: vertical, horizontal or both; a bar of its
// own (ScrollBar, the tsx's second export) takes KeepMounted.
func TestScrollAreaOrientationsAndAStandaloneBar(t *testing.T) {
	body := render(t, with(scrollarea.ScrollArea(scrollarea.Props{Orientation: scrollarea.OrientationHorizontal}), text("wide")))
	require.Equal(t, 1, strings.Count(body, `data-slot="scroll-area-scrollbar"`))
	requireTag(t, body, `data-slot="scroll-area-scrollbar"`, `data-orientation="horizontal"`, "data-horizontal")
	requireTag(t, body, `data-slot="scroll-area-thumb"`, `data-orientation="horizontal"`)

	body = render(t, with(scrollarea.ScrollArea(scrollarea.Props{Orientation: scrollarea.OrientationBoth}), text("wide and tall")))
	require.Equal(t, 2, strings.Count(body, `data-slot="scroll-area-scrollbar"`))
	require.Contains(t, body, `data-orientation="vertical"`)
	require.Contains(t, body, `data-orientation="horizontal"`)

	body = render(t, scrollarea.ScrollBar(scrollarea.ScrollBarProps{Orientation: scrollarea.OrientationHorizontal, KeepMounted: true, Class: "z-10", Attributes: templ.Attributes{"data-x": "y"}}))
	bar := requireTag(t, body, `data-slot="scroll-area-scrollbar"`, `data-orientation="horizontal"`, "data-tui-scroll-area-keep-mounted", "z-10", `data-x="y"`)
	require.NotContains(t, bar, " hidden", "a kept bar is in the DOM from the first paint")
}
