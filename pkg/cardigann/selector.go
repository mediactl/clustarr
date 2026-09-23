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

package cardigann

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/antchfx/xmlquery"
	"github.com/tidwall/gjson"
)

// ResponseType selects which of the three selector backends a Doc uses.
type ResponseType int

const (
	ResponseHTML ResponseType = iota
	ResponseJSON
	ResponseXML
)

// jsonBracketIndex rewrites Cardigann's `foo[0].bar` array-index syntax
// (0dayfiles-api.yml's title_filename: `files[0].name`) into gjson's own
// `foo.0.bar` dot-index syntax before every JSON selector lookup: verified
// against gjson v1.19.0, `Get(json, "files[0].name")` does not match at
// all (bracket indices are not gjson path syntax), only `"files.0.name"`
// does.
var jsonBracketIndex = regexp.MustCompile(`\[(\d+)\]`)

// jsonPath rewrites a Cardigann JSON selector into a gjson path (see
// jsonBracketIndex). Leading dots are dropped, as Prowlarr's HandleJsonSelector drops them
// (Selector.TrimStart('.')): ".title" is "title", and the ".." prefix that
// means "the parent row" (see searchRow) has already chosen which Doc the
// path runs against by the time it gets here. Left in, a leading ".." is
// gjson's JSON Lines syntax and matches nothing a Cardigann author meant.
func jsonPath(selector string) string {
	return jsonBracketIndex.ReplaceAllString(strings.TrimLeft(selector, "."), ".$1")
}

// Doc wraps one parsed response body (or a sub-node of one) so
// SelectorBlock evaluation, row iteration and the filter chain never
// branch on the underlying parser themselves.
type Doc struct {
	rt   ResponseType
	html *goquery.Selection
	json gjson.Result
	xml  *xmlquery.Node
}

// ParseDoc parses body as rt.
func ParseDoc(rt ResponseType, body []byte) (Doc, error) {
	switch rt {
	case ResponseHTML:
		d, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
		if err != nil {
			return Doc{}, fmt.Errorf("cardigann: parse html: %w", err)
		}
		return Doc{rt: rt, html: d.Selection}, nil
	case ResponseJSON:
		if !gjson.ValidBytes(body) {
			return Doc{}, fmt.Errorf("cardigann: invalid json response")
		}
		return Doc{rt: rt, json: gjson.ParseBytes(body)}, nil
	case ResponseXML:
		n, err := xmlquery.Parse(bytes.NewReader(body))
		if err != nil {
			return Doc{}, fmt.Errorf("cardigann: parse xml: %w", err)
		}
		return Doc{rt: rt, xml: n}, nil
	default:
		return Doc{}, fmt.Errorf("cardigann: unknown response type %d", rt)
	}
}

// Select narrows d to the first match of selector (CSS for HTML, a gjson
// path for JSON, an XPath expression for XML); ok is false on no match.
func (d Doc) Select(selector string) (Doc, bool) {
	switch d.rt {
	case ResponseHTML:
		if d.html == nil {
			return Doc{}, false
		}
		sel := d.html.Find(selector)
		if sel.Length() == 0 {
			return Doc{}, false
		}
		return Doc{rt: d.rt, html: sel}, true
	case ResponseJSON:
		r := d.json.Get(jsonPath(selector))
		if !r.Exists() {
			return Doc{}, false
		}
		return Doc{rt: d.rt, json: r}, true
	case ResponseXML:
		if d.xml == nil {
			return Doc{}, false
		}
		n := xmlquery.FindOne(d.xml, selector)
		if n == nil {
			return Doc{}, false
		}
		return Doc{rt: d.rt, xml: n}, true
	}
	return Doc{}, false
}

// Rows returns every match of selector as its own Doc — the HTML/JSON/XML
// equivalents of goquery's *Selection.Each, gjson's array iteration and
// xmlquery.Find.
func (d Doc) Rows(selector string) []Doc {
	switch d.rt {
	case ResponseHTML:
		if d.html == nil {
			return nil
		}
		var out []Doc
		d.html.Find(selector).Each(func(_ int, s *goquery.Selection) { out = append(out, Doc{rt: d.rt, html: s}) })
		return out
	case ResponseJSON:
		var out []Doc
		d.json.Get(jsonPath(selector)).ForEach(func(_, v gjson.Result) bool {
			out = append(out, Doc{rt: d.rt, json: v})
			return true
		})
		return out
	case ResponseXML:
		if d.xml == nil {
			return nil
		}
		var out []Doc
		for _, n := range xmlquery.Find(d.xml, selector) {
			out = append(out, Doc{rt: d.rt, xml: n})
		}
		return out
	}
	return nil
}

// elements returns each element of a JSON array as its own Doc (nil for
// anything else) -- rows.multiple's expansion.
func (d Doc) elements() []Doc {
	if d.rt != ResponseJSON || !d.json.IsArray() {
		return nil
	}
	var out []Doc
	d.json.ForEach(func(_, v gjson.Result) bool {
		out = append(out, Doc{rt: d.rt, json: v})
		return true
	})
	return out
}

// absorb moves every child node of other (text included, as Prowlarr moves
// ChildNodes) to the end of d: rows.after's merge. HTML and XML only; the
// emptied row stays in the document, where it can still be walked past by
// prevRow.
func (d Doc) absorb(other Doc) {
	switch d.rt {
	case ResponseHTML:
		if d.html != nil && other.html != nil {
			d.html.AppendSelection(other.html.Contents())
		}
	case ResponseXML:
		if d.xml == nil || other.xml == nil {
			return
		}
		for c := other.xml.FirstChild; c != nil; {
			next := c.NextSibling
			xmlquery.RemoveFromTree(c)
			xmlquery.AddChild(d.xml, c)
			c = next
		}
	}
}

// prevRow is the row before d for rows.dateheaders: its previous element
// sibling, or -- when d is first in its parent -- the parent's previous
// element sibling (CardigannParser's PreviousElementSibling/ParentElement
// walk). ok is false when there is none. JSON rows have no siblings.
func (d Doc) prevRow() (Doc, bool) {
	switch d.rt {
	case ResponseHTML:
		if d.html == nil || d.html.Length() == 0 {
			return Doc{}, false
		}
		cur := d.html.First()
		prev := cur.Prev()
		if prev.Length() == 0 {
			prev = cur.Parent().Prev()
		}
		if prev.Length() == 0 {
			return Doc{}, false
		}
		return Doc{rt: d.rt, html: prev}, true
	case ResponseXML:
		if d.xml == nil {
			return Doc{}, false
		}
		if prev := prevElement(d.xml); prev != nil {
			return Doc{rt: d.rt, xml: prev}, true
		}
		if d.xml.Parent != nil {
			if prev := prevElement(d.xml.Parent); prev != nil {
				return Doc{rt: d.rt, xml: prev}, true
			}
		}
	}
	return Doc{}, false
}

// prevElement is n's nearest preceding sibling that is an element.
func prevElement(n *xmlquery.Node) *xmlquery.Node {
	for p := n.PrevSibling; p != nil; p = p.PrevSibling {
		if p.Type == xmlquery.ElementNode {
			return p
		}
	}
	return nil
}

// matches reports whether d itself (not a descendant) matches selector:
// Prowlarr's HandleSelector tries dom.Matches(selector) before
// QuerySelector, so a dateheaders selector naming the header row's own
// class matches the header row. HTML only; XPath has no self-match to try.
func (d Doc) matches(selector string) bool {
	return d.rt == ResponseHTML && d.html != nil && d.html.Is(selector)
}

// Text reads d's text (attribute == "" for HTML, ignored for JSON) or a
// named attribute (HTML attribute, or one more gjson/xpath descent step for
// JSON/XML — matches RowsBlock.Attribute's "descend one more level"
// semantics from note §3.6); ok is false when attribute doesn't exist.
//
// For a JSON array with no further attribute descent, the elements are
// joined with "," rather than returned as raw JSON text (e.g.
// `["Action","Sci-Fi"]`): this is what makes a plain re_replace/split
// filter chain on a genre-like field operate on sane plain text instead of
// bracket-and-quote-laden JSON source (0dayfiles-api.yml's genre field,
// `selector: meta.genres`, relies on this).
func (d Doc) Text(attribute string) (string, bool) {
	switch d.rt {
	case ResponseHTML:
		if d.html == nil || d.html.Length() == 0 {
			return "", false
		}
		if attribute == "" {
			return strings.TrimSpace(d.html.Text()), true
		}
		return d.html.Attr(attribute)
	case ResponseJSON:
		if attribute != "" {
			sub, ok := d.Select(attribute)
			if !ok {
				return "", false
			}
			return sub.Text("")
		}
		if !d.json.Exists() {
			return "", false
		}
		if d.json.IsArray() {
			var parts []string
			d.json.ForEach(func(_, v gjson.Result) bool { parts = append(parts, v.String()); return true })
			return strings.Join(parts, ","), true
		}
		return d.json.String(), true
	case ResponseXML:
		if d.xml == nil {
			return "", false
		}
		if attribute == "" {
			return strings.TrimSpace(d.xml.InnerText()), true
		}
		for _, a := range d.xml.Attr {
			if a.Name.Local == attribute {
				return a.Value, true
			}
		}
		return "", false
	}
	return "", false
}

// Extract evaluates b against d: Text (literal/template) short-circuits
// Selector and Case; otherwise Selector+Attribute narrows d, Remove strips a
// nested selector first (HTML only), Case maps the selection (see
// caseValue), then Filters run in order. ok is false when Selector matched
// nothing and b.Optional with no Default — callers use this to implement the
// non-optional-field-drops-the-row rule (note §3.8).
func (b SelectorBlock) Extract(ctx context.Context, d Doc, tc *TemplateContext) (string, bool, error) {
	raw, ok, err := b.extractRaw(ctx, d, tc)
	if err != nil || !ok {
		return "", false, err
	}
	for _, f := range b.Filters {
		fn, known := Filters[f.Name]
		if !known {
			return "", false, fmt.Errorf("cardigann: unknown filter %q", f.Name)
		}
		raw, err = fn(ctx, raw, []string(f.Args), tc)
		if err != nil {
			return "", false, fmt.Errorf("cardigann: filter %q: %w", f.Name, err)
		}
	}
	return raw, true, nil
}

// extractRaw resolves Text, or Selector+Remove then Case or Attribute, into
// a raw string before Filters run -- Prowlarr's HandleSelector /
// HandleJsonSelector order: a Text block returns its template and never
// consults Case.
func (b SelectorBlock) extractRaw(_ context.Context, d Doc, tc *TemplateContext) (string, bool, error) {
	if b.Text != nil {
		rendered, err := render(string(*b.Text), tc)
		if err != nil {
			return "", false, err
		}
		return rendered, true, nil
	}

	sel := d
	if b.Selector != "" {
		// Selector is itself a template (e.g. 1337x's rows.selector,
		// `tr:has(...){{ if .Config.uploader }}...{{ end }}`, and its
		// download selectors, `a[href*="{{ .Config.primarydownloadlink }}"]`)
		// — render it before handing it to the CSS/gjson/XPath backend.
		renderedSelector, err := render(b.Selector, tc)
		if err != nil {
			return "", false, err
		}
		found, ok := d.Select(renderedSelector)
		if !ok {
			return b.optionalFallback()
		}
		sel = found
	}
	if b.Remove != "" && sel.rt == ResponseHTML && sel.html != nil {
		sel.html.Find(b.Remove).Remove()
	}
	if len(b.Case) > 0 && sel.rt == ResponseHTML {
		return b.htmlCase(sel, tc)
	}
	raw, ok := sel.Text(b.Attribute)
	if !ok {
		return b.optionalFallback()
	}
	if len(b.Case) > 0 {
		return b.valueCase(raw, tc)
	}
	return raw, true, nil
}

// htmlCase is Case on an HTML selection. Each key is a CSS selector, tried
// in file order against the selection itself and then its descendants
// (Prowlarr: `selection.Matches(key) || QuerySelector(selection, key) !=
// null`); the first that matches renders its value. "*" is simply the
// universal selector, so written last it is the fallback. When nothing
// matches the field is missing, as Prowlarr's null is. This replaced a
// value-equality lookup that compared each CSS selector to the row's text,
// so a freeleech `img.free: 0` never matched and every HTML tracker's
// volume factors fell through to "*".
func (b SelectorBlock) htmlCase(sel Doc, tc *TemplateContext) (string, bool, error) {
	for _, c := range b.Case {
		if sel.html.Is(c.Key) || sel.html.Find(c.Key).Length() > 0 {
			rendered, err := render(string(c.Value), tc)
			if err != nil {
				return "", false, err
			}
			return rendered, true, nil
		}
	}
	return b.optionalFallback()
}

// valueCase is Case on a JSON or XML value: the first key equal to raw, or
// "*", in file order, renders its value; no match keeps raw (Prowlarr's
// HandleJsonSelector). Keys are compared verbatim, which matches both the
// corpus's case-sensitive keys (freeleech's "100%"/"0%") and its
// True/False-as-bareword-YAML-key idiom, which goccy/go-yaml decodes to the
// lower-case "true"/"false" gjson's boolean String() also produces. XML
// takes this path too: Prowlarr matches XML case keys as CSS selectors over
// its DOM, but this package queries XML with XPath, so a CSS key cannot be
// evaluated there.
func (b SelectorBlock) valueCase(raw string, tc *TemplateContext) (string, bool, error) {
	for _, c := range b.Case {
		if c.Key == raw || c.Key == "*" {
			rendered, err := render(string(c.Value), tc)
			if err != nil {
				return "", false, err
			}
			return rendered, true, nil
		}
	}
	return raw, true, nil
}

// optionalFallback is what Extract returns when a selector matched
// nothing: b.Default when set (implies b.Optional per the schema's
// dependentRequired), else (false, nil) — the caller (RowsBlock iteration
// / field mapping in search.go) is what turns a non-optional miss into
// "drop this row", not Extract itself.
func (b SelectorBlock) optionalFallback() (string, bool, error) {
	if b.Default != nil {
		return string(*b.Default), true, nil
	}
	return "", false, nil
}
