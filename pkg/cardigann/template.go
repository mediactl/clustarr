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
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"

	"golang.org/x/text/encoding"
)

// QueryVars is the subset of a Query exposed to templates as .Query.*.
type QueryVars struct {
	Type, Q, Keywords string

	IMDBID, IMDBIDShort, TVDBID, TMDBID, TVMazeID string
	TraktID, DoubanID                             string

	Season, Ep, Episode, Year, Genre string
	Album, Artist, Label, Track      string
	Author, Title, Publisher         string
}

// TemplateContext is the "." a Definition's templates (paths, inputs,
// selectors, field text) render against.
type TemplateContext struct {
	// Config holds resolved settings, always including "sitelink" =
	// Config.BaseURL.
	Config map[string]any

	Keywords string
	Query    QueryVars

	// Categories are the tracker category ids the current request
	// targets, as strings — for {{ range .Categories }}.
	Categories []string

	Result map[string]string

	// DownloadUri is the link Engine.Download is resolving, as
	// `.DownloadUri.*` (Prowlarr's AddTemplateVariablesFromUri(variables,
	// link, ".DownloadUri")); zero outside Download. The corpus reads
	// .DownloadUri.Query.id, .AbsoluteUri, .AbsolutePath and .PathAndQuery
	// in download selectors and headers. (It was DownloadURI, a *url.URL
	// nothing set, so the 13 definitions using it failed to render.)
	DownloadUri URIVars

	// True/False are sentinel strings for checkbox comparisons:
	// True="true", False="".
	True, False string

	Today struct{ Year int }

	// Now is the clock every date-relative filter and .Today.Year reads;
	// Engine.templateContext sets it from Engine.Now (defaulting to
	// time.Now when unset) so tests are deterministic.
	Now time.Time

	// enc is the definition's character set (nil for UTF-8). It travels
	// with the context because every consumer of it already has one: the
	// request encoders, the response decoder and the urlencode/urldecode
	// filters (see encoding.go).
	enc encoding.Encoding
}

// URIVars is a URL as Cardigann templates see it: .NET System.Uri's
// property names, and Query as a map, first value per key.
type URIVars struct {
	AbsoluteUri  string // .NET System.Uri spelling: the name templates use
	AbsolutePath string
	Scheme       string
	Host         string
	Port         string
	PathAndQuery string
	Query        map[string]string
}

// uriVars builds URIVars for u. Port is the scheme's default when u has
// none, as System.Uri.Port reports it.
func uriVars(u *url.URL) URIVars {
	v := URIVars{
		AbsoluteUri:  u.String(),
		AbsolutePath: u.EscapedPath(),
		Scheme:       u.Scheme,
		Host:         u.Hostname(),
		Port:         u.Port(),
		PathAndQuery: u.RequestURI(),
		Query:        map[string]string{},
	}
	if v.AbsolutePath == "" {
		v.AbsolutePath = "/"
	}
	if v.Port == "" {
		switch u.Scheme {
		case "https":
			v.Port = "443"
		case "http":
			v.Port = "80"
		}
	}
	for k, vals := range u.Query() {
		if len(vals) > 0 {
			v.Query[k] = vals[0]
		}
	}
	return v
}

// effectiveNow returns tc.Now, falling back to time.Now() for a
// TemplateContext built without going through Engine (matching this
// field's own doc: "defaults to time.Now()").
func (tc *TemplateContext) effectiveNow() time.Time {
	if tc == nil || tc.Now.IsZero() {
		return time.Now()
	}
	return tc.Now
}

// Config is the caller-supplied indexer configuration: base URL and
// resolved setting values. The indexer controller builds it from
// Indexer.spec.settings merged with the decoded SecretRef Secret;
// pkg/cardigann never reads a Kubernetes object.
type Config struct {
	BaseURL string
	Values  map[string]any // from Definition.ResolveSettings
	Session *Session       // nil until Engine.Login; required by Search/Download when Definition.Login != nil
}

// funcMap is Cardigann's own small, closed template dialect: text/template
// already provides if/else/range/and/or/eq/ne, so only two additions are
// needed. sprig/v3's FuncMap is deliberately not merged in — Cardigann's
// dialect is small and closed, and exposing sprig's ~100 extra functions to
// a definition would silently accept syntax no real Prowlarr definition
// uses and let a future custom IndexerDefinition depend on Clustarr-only
// template behaviour that breaks compatibility with upstream Cardigann
// files.
var funcMap = template.FuncMap{
	"join": strings.Join,
	// re_replace runs pattern and repl as .NET does (regex.go), like the
	// re_replace filter.
	"re_replace": func(value, pattern, repl string) (string, error) {
		re, err := compileRegex(pattern)
		if err != nil {
			return "", fmt.Errorf("cardigann: re_replace: %w", err)
		}
		out, err := regexReplaceAll(re, value, repl)
		if err != nil {
			return "", fmt.Errorf("cardigann: re_replace: %w", err)
		}
		return out, nil
	},
}

// render evaluates tmplText as a Go text/template against tc.
//
// On a parse failure, it retries once against balanceActionParens(tmplText)
// — verified against the required corpus, 1337x.yml's own second
// search.paths entry (the TV page) contains a stray extra ")" —
// `{{ if and (.Keywords) (eq .Config.disablesort .False)) }}` — a real
// authoring typo in the seeded, unmodifiable fixture. Go's text/template
// parser rejects it outright ("unexpected right paren"); Cardigann's own
// C# expression evaluator evidently tolerates it, since this file is a
// real, in-use definition. Retrying against the paren-balanced text is
// this package's only way to still search all four of 1337x's paths
// without editing test/data/cardigann/1337x.yml, which Task B0 seeded and
// this task may not modify.
func render(tmplText string, tc *TemplateContext) (string, error) {
	return renderModified(tmplText, tc, nil)
}

// modifierFunc is the template function name renderModified binds its
// modifier to. A definition cannot name it: it is only ever added to the
// parse tree, after parsing.
const modifierFunc = "_cardigannModifier"

// renderModified is render with ApplyGoTemplateText's TemplateTextModifier
// (Prowlarr CardigannBase, Jackett CardigannIndexer): modifier, when
// non-nil, is applied to the text every substitution produces -- a
// variable, a range element, a join or re_replace result -- and never to
// the template's literal text, so a search path's "/" and "?" survive while
// a keyword's are escaped. It is done the way html/template escapes: a
// call to modifier is appended to the pipeline of every output action in
// the parse tree.
func renderModified(tmplText string, tc *TemplateContext, modifier func(string) string) (string, error) {
	t, err := parseTemplate(tmplText, modifier)
	if err != nil {
		return "", err
	}
	if modifier != nil && t.Tree != nil {
		modifyOutputs(t.Root)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, tc); err != nil {
		return "", fmt.Errorf("cardigann: template exec %q: %w", tmplText, err)
	}
	return buf.String(), nil
}

// parseTemplate parses tmplText as render does: adapted by goTemplateText,
// then one retry against balanceActionParens.
func parseTemplate(tmplText string, modifier func(string) string) (*template.Template, error) {
	parseText := func(text string) (*template.Template, error) {
		t := template.New("cardigann").Funcs(funcMap)
		if modifier != nil {
			t = t.Funcs(template.FuncMap{modifierFunc: modifier})
		}
		return t.Parse(text)
	}
	text := goTemplateText(tmplText)
	t, err := parseText(text)
	if err != nil {
		if fixed, ok := balanceActionParens(text); ok {
			if t2, err2 := parseText(fixed); err2 == nil {
				return t2, nil
			}
		}
		return nil, fmt.Errorf("cardigann: template %q: %w", tmplText, err)
	}
	return t, nil
}

// modifyOutputs appends a modifierFunc call to every output action under n:
// an action that declares a variable prints nothing and is left alone, and
// an if/range/with condition is not output, so only the branches are
// walked.
func modifyOutputs(n parse.Node) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			modifyOutputs(c)
		}
	case *parse.ActionNode:
		if len(n.Pipe.Decl) > 0 {
			return
		}
		n.Pipe.Cmds = append(n.Pipe.Cmds, &parse.CommandNode{
			NodeType: parse.NodeCommand,
			Pos:      n.Pos,
			Args:     []parse.Node{parse.NewIdentifier(modifierFunc).SetPos(n.Pos)},
		})
	case *parse.IfNode:
		modifyOutputs(n.List)
		modifyOutputs(n.ElseList)
	case *parse.RangeNode:
		modifyOutputs(n.List)
		modifyOutputs(n.ElseList)
	case *parse.WithNode:
		modifyOutputs(n.List)
		modifyOutputs(n.ElseList)
	}
}

// webURLEncode is .NET's WebUtility.UrlEncode, the modifier Prowlarr and
// Jackett render a search path and a $raw input with: the UTF-8 bytes of s,
// with ASCII letters, digits and "-_.!*()" kept, a space as "+", and every
// other byte as %XX in upper-case hex.
func webURLEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '*', c == '(', c == ')':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

// nonIdentKey matches a .Config or .Result key the text/template lexer
// cannot read as a field name, with the $ of a root-relative reference:
// one containing a hyphen (nyaasi.yml's search inputs are
// `{{ .Config.cat-id }}` and `{{ .Config.filter-id }}`) or starting with a
// digit (the `{{ .Config.2facode }}` login input of several NexusPHP
// definitions). replaceKeys keeps only the matches that are one of the two.
var nonIdentKey = regexp.MustCompile(`(\$?)\.(Config|Result)\.([A-Za-z0-9_]+(?:-[A-Za-z0-9_]+)*)`)

// goTemplateText adapts Prowlarr's template dialect to text/template, inside
// each {{ ... }} action and nowhere else:
//
//   - .Config.<key> and .Result.<key> whose key is not a Go identifier become
//     (index .Config "<key>"). Prowlarr and Jackett resolve the setting by its
//     whole name; text/template lexes .Config.cat-id as .Config.cat and a
//     stray "-" ("bad character U+002D") and .Config.2facode as a number.
//   - a "..." literal Go cannot unquote becomes a `...` raw string. Prowlarr
//     takes re_replace's pattern verbatim, so nyaasi's "\b0(\d{1})\b" is a
//     regex; Go rejects \d as an escape ("invalid syntax"). A literal Go can
//     unquote keeps Go's meaning, so no template that parsed before changes.
//
// Either way the template failed to parse, so every search of such a
// definition failed before a request was sent.
func goTemplateText(tmplText string) string {
	if !strings.ContainsAny(tmplText, "-\\0123456789") {
		return tmplText
	}
	var b strings.Builder
	rest := tmplText
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}}")
		if end < 0 {
			b.WriteString(rest)
			break
		}
		end += start + 2
		b.WriteString(rest[:start])
		b.WriteString(goTemplateAction(rest[start:end]))
		rest = rest[end:]
	}
	return b.String()
}

// goTemplateAction applies goTemplateText's two rewrites to one action:
// replaceKeys to the text outside its "...", `...` and '...' literals, and
// rawLiteral to each "..." literal.
func goTemplateAction(action string) string {
	var b strings.Builder
	run := 0
	for i := 0; i < len(action); i++ {
		quote := action[i]
		if quote != '"' && quote != '`' && quote != '\'' {
			continue
		}
		b.WriteString(replaceKeys(action[run:i]))
		j := i + 1
		for j < len(action) && action[j] != quote {
			if action[j] == '\\' && quote != '`' {
				j++
			}
			j++
		}
		j = min(j+1, len(action))
		lit := action[i:j]
		if quote == '"' {
			lit = rawLiteral(lit)
		}
		b.WriteString(lit)
		run = j
		i = j - 1
	}
	b.WriteString(replaceKeys(action[run:]))
	return b.String()
}

// rawLiteral returns lit, a "..." literal, as a `...` raw string when Go
// cannot unquote it and its body holds no backquote; otherwise lit itself.
func rawLiteral(lit string) string {
	if len(lit) < 2 || lit[len(lit)-1] != '"' || !strings.Contains(lit, "\\") {
		return lit
	}
	if _, err := strconv.Unquote(lit); err == nil {
		return lit
	}
	body := lit[1 : len(lit)-1]
	if strings.Contains(body, "`") {
		return lit
	}
	return "`" + body + "`"
}

// replaceKeys rewrites each nonIdentKey match in s whose key is not a Go
// identifier and which starts a field chain; one that continues another
// chain (.Foo.Config.a-b, $x.Config.a-b) is not a reference to the
// context's Config or Result and is left alone.
func replaceKeys(s string) string {
	matches := nonIdentKey.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		key := s[m[6]:m[7]]
		if !strings.Contains(key, "-") && (key[0] < '0' || key[0] > '9') {
			continue
		}
		if m[0] > 0 {
			switch c := s[m[0]-1]; {
			case c == '_', c == '.', c == ')', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
				continue
			}
		}
		b.WriteString(s[last:m[0]])
		fmt.Fprintf(&b, "(index %s.%s %q)", s[m[2]:m[3]], s[m[4]:m[5]], key)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// balanceActionParens finds every {{ ... }} action in tmplText and, only
// where its parenthesis count is unbalanced with strictly more ")" than
// "(", collapses just enough doubled ")" runs to balance it. A block that
// already balances, or has more "(" than ")" (a different, real error
// this package should not try to paper over), is left untouched. ok is
// true only when at least one action was actually changed.
func balanceActionParens(tmplText string) (string, bool) {
	var b strings.Builder
	changed := false
	rest := tmplText
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}}")
		if end < 0 {
			b.WriteString(rest)
			break
		}
		end += start + 2
		b.WriteString(rest[:start])
		action := rest[start:end]

		opens, closes := strings.Count(action, "("), strings.Count(action, ")")
		if closes > opens {
			fixed := action
			for range closes - opens {
				fixed = strings.Replace(fixed, "))", ")", 1)
			}
			if strings.Count(fixed, "(") == strings.Count(fixed, ")") {
				action = fixed
				changed = true
			}
		}
		b.WriteString(action)
		rest = rest[end:]
	}
	return b.String(), changed
}

// ResolveSettings applies each SettingsField's type semantics to raw
// (spec.settings merged with the secret): checkbox -> "true"/"" (matching
// TemplateContext.True/False so `eq .Config.x .False` type-checks),
// select -> the chosen option key verbatim, text/password -> verbatim,
// info* -> never present in Config. Always injects "sitelink" = baseURL.
//
// Any raw key that is not a declared setting name passes through
// unchanged: login.cookies names arbitrary cookie names, not settings, and
// loginCookie reads those straight out of Config.Values.
func (d *Definition) ResolveSettings(baseURL string, raw map[string]string) (map[string]any, error) {
	declared := make(map[string]bool, len(d.Settings))
	values := make(map[string]any, len(d.Settings)+len(raw)+1)

	for _, s := range d.Settings {
		declared[s.Name] = true
		switch s.Type {
		case "checkbox":
			v, ok := raw[s.Name]
			if !ok {
				v = string(s.Default)
			}
			if v == "true" {
				values[s.Name] = "true"
			} else {
				values[s.Name] = ""
			}
		case "select":
			if v, ok := raw[s.Name]; ok {
				if _, known := s.Options[v]; known {
					values[s.Name] = v
					continue
				}
			}
			values[s.Name] = string(s.Default)
		case "text", "password":
			if v, ok := raw[s.Name]; ok {
				values[s.Name] = v
			} else {
				values[s.Name] = string(s.Default)
			}
		default:
			// info, info_category_8000, info_cookie, info_flaresolverr,
			// info_useragent, or anything unrecognised: never appears in
			// Config.
		}
	}

	for k, v := range raw {
		if declared[k] {
			continue
		}
		values[k] = v
	}

	values["sitelink"] = baseURL
	return values, nil
}

// NewConfig is ResolveSettings plus wrapping into a Config. baseURL passes
// through Definition.SiteLink first, so an indexer configured with one of
// the definition's legacylinks talks to its current address instead -- and
// .Config.sitelink, which templates build absolute URLs from, says so too.
func NewConfig(def *Definition, baseURL string, raw map[string]string) (Config, error) {
	baseURL = def.SiteLink(baseURL)
	values, err := def.ResolveSettings(baseURL, raw)
	if err != nil {
		return Config{}, err
	}
	return Config{BaseURL: baseURL, Values: values}, nil
}
