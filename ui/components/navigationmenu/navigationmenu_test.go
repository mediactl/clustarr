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

package navigationmenu_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui/components/navigationmenu"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, c.Render(context.Background(), &b))
	return b.String()
}

// with renders parent around children, the way a templ block nests them.
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

// tagWith returns the opening tag of the first element carrying attr.
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

func menu(p navigationmenu.Props) templ.Component {
	return with(navigationmenu.NavigationMenu(p),
		with(navigationmenu.List(),
			with(navigationmenu.Item(navigationmenu.ItemProps{Value: "library"}),
				with(navigationmenu.Trigger(), text("Library")),
				with(navigationmenu.Content(),
					with(navigationmenu.Link(navigationmenu.LinkProps{Href: "/library/movies", Active: true}), text("Movies")),
					with(navigationmenu.Link(navigationmenu.LinkProps{Href: "/library/tv", CloseOnClick: true}), text("TV")),
				),
			),
			with(navigationmenu.Item(),
				with(navigationmenu.Link(navigationmenu.LinkProps{Href: "/downloads", Class: navigationmenu.TriggerStyle()}), text("Downloads")),
			),
		),
	)
}

// The parts are shadcn's base navigation-menu.tsx over Base UI's
// NavigationMenu, one templ component per part, the data-slot names and
// class strings verbatim, and data-tui-navigation-menu-* for the script.
func TestNavigationMenuRendersShadcnsPartsOverBaseUIs(t *testing.T) {
	body := render(t, menu(navigationmenu.Props{ID: "nav"}))

	root := requireTag(t, body, `data-slot="navigation-menu"`,
		`id="nav"`, "data-tui-navigation-menu", `data-tui-navigation-menu-id="nav"`,
		`data-orientation="horizontal"`, "data-horizontal",
		`data-tui-navigation-menu-delay="50"`, `data-tui-navigation-menu-close-delay="50"`,
		"group/navigation-menu", "relative", "flex", "max-w-max", "items-center", "justify-center")
	require.True(t, strings.HasPrefix(root, "<nav"), "Base UI's Root is a nav: %s", root)
	require.NotContains(t, root, "data-tui-navigation-menu-value=", "nothing is open until the reader opens it")

	list := requireTag(t, body, `data-slot="navigation-menu-list"`, "group", "flex", "flex-1", "list-none", "items-center", "justify-center", "gap-0")
	require.True(t, strings.HasPrefix(list, "<ul"), "the list is a ul: %s", list)

	item := requireTag(t, body, `data-tui-navigation-menu-value="library"`, `data-slot="navigation-menu-item"`, "relative")
	require.True(t, strings.HasPrefix(item, "<li"), "an item is an li: %s", item)

	trigger := requireTag(t, body, `data-slot="navigation-menu-trigger"`,
		`type="button"`, "data-tui-navigation-menu-trigger", `data-tui-navigation-menu-id="nav"`, `data-tui-navigation-menu-value="library"`,
		`aria-expanded="false"`, `aria-controls="nav-content-library"`,
		"group/navigation-menu-trigger", "inline-flex", "h-9", "w-max", "rounded-md", "px-4", "py-2", "text-sm", "font-medium",
		"hover:bg-muted", "focus:bg-muted", "focus-visible:ring-3", "data-popup-open:bg-muted/50", "data-open:bg-muted/50")
	require.True(t, strings.HasPrefix(trigger, "<button"), "the trigger is a button: %s", trigger)
	require.NotRegexp(t, ` data-popup-open[ >]`, trigger, "closed at first (the attribute, not the data-popup-open: classes)")
	require.Contains(t, trigger, navigationmenu.TriggerStyle(), "the trigger wears TriggerStyle, which a link styled as a trigger shares")
	at := strings.Index(body, `data-slot="navigation-menu-trigger"`)
	chevron := tagWith(t, body[at:], "group-data-popup-open/navigation-menu-trigger:rotate-180")
	require.True(t, strings.HasPrefix(chevron, "<svg"), "the trigger ends with the chevron icon: %s", chevron)
	require.Contains(t, chevron, `aria-hidden="true"`)
	require.Contains(t, chevron, "size-3")

	content := requireTag(t, body, `data-slot="navigation-menu-content"`,
		`id="nav-content-library"`, "data-tui-navigation-menu-content", `data-tui-navigation-menu-id="nav"`, `data-tui-navigation-menu-value="library"`,
		" hidden", "data-closed", "h-full", "w-auto", "p-2", "pr-2.5", "data-starting-style:opacity-0", "data-ending-style:opacity-0",
		"data-starting-style:data-[activation-direction=left]:translate-x-[-50%]", "data-ending-style:data-[activation-direction=right]:translate-x-[-50%]")
	require.NotContains(t, content, "data-open")
	require.Less(t, strings.Index(body, `data-slot="navigation-menu-item"`), strings.Index(body, `data-slot="navigation-menu-content"`), "the content renders inside its item; the script moves it into the viewport when it opens")

	link := requireTag(t, body, `href="/library/movies"`, `data-slot="navigation-menu-link"`, `data-active="true"`,
		"flex", "items-center", "gap-1.5", "rounded-md", "p-2", "text-sm", "hover:bg-muted", "focus:bg-muted", "data-[active=true]:bg-muted/50", "in-data-[slot=navigation-menu-content]:rounded-sm")
	require.True(t, strings.HasPrefix(link, "<a"), "a link is an a: %s", link)
	require.NotContains(t, link, "close-on-click")
	requireTag(t, body, `href="/library/tv"`, "data-tui-navigation-menu-close-on-click")
	require.NotContains(t, tagWith(t, body, `href="/library/tv"`), "data-active")
	styled := requireTag(t, body, `href="/downloads"`, `data-slot="navigation-menu-link"`, "group/navigation-menu-trigger", "h-9")
	require.NotContains(t, styled, "data-active")

	// The positioner, popup and viewport render once at the root's end,
	// after the list, as the tsx's NavigationMenuPositioner does; hidden
	// until the script opens an item, and positioned against its trigger.
	require.Less(t, strings.LastIndex(body, `data-slot="navigation-menu-list"`), strings.Index(body, `data-slot="navigation-menu-positioner"`))
	require.Equal(t, 1, strings.Count(body, `data-slot="navigation-menu-positioner"`))
	positioner := requireTag(t, body, `data-slot="navigation-menu-positioner"`,
		"data-tui-navigation-menu-positioner", `data-tui-navigation-menu-id="nav"`, " hidden", "data-closed",
		`data-tui-navigation-menu-side="bottom"`, `data-tui-navigation-menu-align="start"`,
		`data-tui-navigation-menu-side-offset="8"`, `data-tui-navigation-menu-align-offset="0"`,
		"absolute", "isolate", "z-50", "h-(--positioner-height)", "w-(--positioner-width)", "max-w-(--available-width)",
		"transition-[top,left,right,bottom]", "data-instant:transition-none")
	require.NotContains(t, positioner, "data-open")
	popup := requireTag(t, body, `data-slot="navigation-menu-popup"`, "data-tui-navigation-menu-popup", "data-closed",
		"relative", "h-(--popup-height)", "w-(--popup-width)", "origin-(--transform-origin)", "rounded-lg", "bg-popover", "text-popover-foreground",
		"shadow", "ring-1", "ring-foreground/10", "data-ending-style:scale-90", "data-ending-style:opacity-0", "data-starting-style:scale-90", "data-starting-style:opacity-0")
	require.True(t, strings.HasPrefix(popup, "<nav"), "Base UI's Popup is a nav: %s", popup)
	requireTag(t, body, `data-slot="navigation-menu-viewport"`, "data-tui-navigation-menu-viewport", "relative", "size-full", "overflow-hidden")
	require.Less(t, strings.Index(body, `data-slot="navigation-menu-popup"`), strings.Index(body, `data-slot="navigation-menu-viewport"`), "the viewport sits inside the popup")
}

// An item without a Value still ties its trigger and content together.
func TestNavigationMenuItemGetsAValueWhenNoneIsGiven(t *testing.T) {
	body := render(t, with(navigationmenu.NavigationMenu(),
		with(navigationmenu.List(),
			with(navigationmenu.Item(),
				with(navigationmenu.Trigger(), text("Settings")),
				with(navigationmenu.Content(), text("panel")),
			),
		),
	))
	item := tagWith(t, body, `data-slot="navigation-menu-item"`)
	i := strings.Index(item, `data-tui-navigation-menu-value="`)
	require.GreaterOrEqual(t, i, 0, "the item minted a value: %s", item)
	value := item[i+len(`data-tui-navigation-menu-value="`):]
	value = value[:strings.Index(value, `"`)]
	require.NotEmpty(t, value)
	requireTag(t, body, `data-slot="navigation-menu-trigger"`, `data-tui-navigation-menu-value="`+value+`"`)
	requireTag(t, body, `data-slot="navigation-menu-content"`, `data-tui-navigation-menu-value="`+value+`"`)
	root := tagWith(t, body, `data-slot="navigation-menu"`)
	require.Regexp(t, `data-tui-navigation-menu-id="id-[A-Za-z0-9]+"`, root, "the root minted its id too")
}

// The root's options: Base UI's orientation, delay and closeDelay, the
// positioner's side, align and offsets, and Value or DefaultValue for an
// item open from the first paint.
func TestNavigationMenuOptionsReachTheScript(t *testing.T) {
	value := "library"
	body := render(t, menu(navigationmenu.Props{
		ID: "nav", Orientation: navigationmenu.OrientationVertical, Delay: 200, CloseDelay: 100,
		Side: navigationmenu.SideRight, Align: navigationmenu.AlignCenter, SideOffset: 4, AlignOffset: -2,
		Value: &value,
	}))
	requireTag(t, body, `data-slot="navigation-menu"`, `data-orientation="vertical"`, "data-vertical",
		`data-tui-navigation-menu-delay="200"`, `data-tui-navigation-menu-close-delay="100"`,
		`data-tui-navigation-menu-value="library"`, "data-tui-navigation-menu-controlled")
	require.NotContains(t, tagWith(t, body, `data-slot="navigation-menu"`), "data-horizontal")
	requireTag(t, body, `data-slot="navigation-menu-positioner"`, `data-tui-navigation-menu-side="right"`, `data-tui-navigation-menu-align="center"`,
		`data-tui-navigation-menu-side-offset="4"`, `data-tui-navigation-menu-align-offset="-2"`)
	require.Regexp(t, ` data-popup-open[ >]`, requireTag(t, body, `data-slot="navigation-menu-trigger"`, `aria-expanded="true"`))
	content := requireTag(t, body, `data-slot="navigation-menu-content"`, "data-open")
	require.NotContains(t, content, " hidden")
	require.NotContains(t, content, "data-closed")

	body = render(t, menu(navigationmenu.Props{ID: "nav", DefaultValue: "library"}))
	root := requireTag(t, body, `data-slot="navigation-menu"`, `data-tui-navigation-menu-value="library"`)
	require.NotContains(t, root, "data-tui-navigation-menu-controlled", "DefaultValue hands the open item to the browser")
	requireTag(t, body, `data-slot="navigation-menu-trigger"`, `aria-expanded="true"`)
}

// Indicator is the tsx's NavigationMenuIndicator: the small diamond the
// script slides under the open trigger.
func TestNavigationMenuIndicator(t *testing.T) {
	body := render(t, with(navigationmenu.NavigationMenu(navigationmenu.Props{ID: "nav"}),
		with(navigationmenu.List(), with(navigationmenu.Item(navigationmenu.ItemProps{Value: "a"}), with(navigationmenu.Trigger(), text("A")), with(navigationmenu.Content(), text("a")))),
		navigationmenu.Indicator(),
	))
	indicator := requireTag(t, body, `data-slot="navigation-menu-indicator"`, "data-tui-navigation-menu-indicator", `data-tui-navigation-menu-id="nav"`,
		`data-state="hidden"`, "top-full", "z-1", "flex", "h-1.5", "items-end", "justify-center", "overflow-hidden",
		"data-[state=hidden]:animate-out", "data-[state=hidden]:fade-out", "data-[state=visible]:animate-in", "data-[state=visible]:fade-in", "absolute")
	require.NotEmpty(t, indicator)
	at := strings.Index(body, `data-slot="navigation-menu-indicator"`)
	requireTag(t, body[at:], "rotate-45", "relative", "top-[60%]", "h-2", "w-2", "rounded-tl-sm", "bg-border", "shadow-md")
}
