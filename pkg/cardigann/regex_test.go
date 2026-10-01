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
	"context"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"text/template/parse"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/indexer/bundle/embedded"
)

// corpusRegex is one regex use found in a bundled definition: a regexp or
// re_replace filter, or a re_replace call inside a template.
type corpusRegex struct {
	file, where, pattern, repl string
	filter                     *FilterBlock // nil for a template call
}

// collectCorpusRegexes walks every field of def -- by reflection, so a block
// added to Definition later is walked too -- and returns every regexp and
// re_replace filter and every re_replace template call with literal
// arguments.
func collectCorpusRegexes(file string, def *Definition) []corpusRegex {
	var out []corpusRegex
	filterType := reflect.TypeOf(FilterBlock{})
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Struct:
			if v.Type() == filterType {
				f := v.Interface().(FilterBlock)
				if f.Name == "regexp" || f.Name == "re_replace" {
					out = append(out, corpusRegex{file: file, where: "filter " + f.Name, pattern: arg(f.Args, 0), repl: arg(f.Args, 1), filter: &f})
				}
				return
			}
			for i := range v.NumField() {
				if v.Type().Field(i).IsExported() {
					walk(v.Field(i))
				}
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				walk(iter.Key())
				walk(iter.Value())
			}
		case reflect.String:
			out = append(out, templateRegexes(file, v.String())...)
		}
	}
	walk(reflect.ValueOf(def))
	return out
}

// templateRegexes parses s as the engine does and returns each
// re_replace call whose pattern and replacement are string literals.
func templateRegexes(file, s string) []corpusRegex {
	if !strings.Contains(s, "re_replace") {
		return nil
	}
	t, err := template.New("corpus").Funcs(funcMap).Parse(s)
	if err != nil {
		fixed, ok := balanceActionParens(s)
		if !ok {
			return nil
		}
		if t, err = template.New("corpus").Funcs(funcMap).Parse(fixed); err != nil {
			return nil
		}
	}
	var out []corpusRegex
	var walk func(n parse.Node)
	walk = func(n parse.Node) {
		switch n := n.(type) {
		case *parse.ListNode:
			if n == nil {
				return
			}
			for _, c := range n.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			walk(n.Pipe)
		case *parse.PipeNode:
			if n == nil {
				return
			}
			for _, c := range n.Cmds {
				walk(c)
			}
		case *parse.CommandNode:
			if len(n.Args) == 4 {
				if id, ok := n.Args[0].(*parse.IdentifierNode); ok && id.Ident == "re_replace" {
					pat, ok1 := n.Args[2].(*parse.StringNode)
					repl, ok2 := n.Args[3].(*parse.StringNode)
					if ok1 && ok2 {
						out = append(out, corpusRegex{file: file, where: "template", pattern: pat.Text, repl: repl.Text})
					}
				}
			}
			for _, a := range n.Args {
				walk(a)
			}
		case *parse.IfNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.RangeNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.WithNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		}
	}
	walk(t.Root)
	return out
}

// TestEveryBundledRegexCompiles runs every regexp and re_replace filter, and
// every re_replace template call, in the 752 bundled Prowlarr definitions
// through the engine's own code path. Prowlarr compiles them as .NET
// regexes; under Go's RE2 136 filter patterns in 106 definitions
// (lookaround, backreferences) failed to compile, failing their field and
// with it the whole search.
func TestEveryBundledRegexCompiles(t *testing.T) {
	fsys, err := embedded.FS()
	require.NoError(t, err)
	defs, _, err := LoadBundle(fsys)
	require.NoError(t, err)
	require.Greater(t, len(defs), 700, "the bundled corpus loads")

	ctx := context.Background()
	tc := &TemplateContext{Config: map[string]any{}, Result: map[string]string{}}
	reReplace := funcMap["re_replace"].(func(string, string, string) (string, error))

	var total, filters, templates int
	failedDefs := map[string]bool{}
	for _, b := range defs {
		for _, r := range collectCorpusRegexes(b.File, b.Definition) {
			total++
			var err error
			if r.filter != nil {
				filters++
				_, err = Filters[r.filter.Name](ctx, "Saison 2 Episode 5 [1080p] 4.2 GB", r.filter.Args, tc)
			} else {
				templates++
				_, err = reReplace("Saison 2 Episode 5", r.pattern, r.repl)
			}
			if err != nil {
				failedDefs[r.file] = true
				t.Errorf("%s: %s %q (replacement %q): %v", r.file, r.where, r.pattern, r.repl, err)
			}
		}
	}
	t.Logf("%d regexes (%d filters, %d template calls); %d definitions failed", total, filters, templates, len(failedDefs))
	assert.Greater(t, filters, 2500, "the walk finds the corpus' filter regexes")
}

// TestReReplaceUsesDotNetSubstitution holds re_replace to .NET's
// substitution grammar, which Prowlarr's definitions are written in: 283
// replacements in 55 bundled definitions read like "S$1E$2". Go's Expand
// reads "$1E" as a group named "1E", which does not exist, so "Saison 2
// Episode 5" became "S5".
func TestReReplaceUsesDotNetSubstitution(t *testing.T) {
	ctx := context.Background()
	tc := &TemplateContext{}
	cases := []struct {
		name, value, pattern, repl, want string
	}{
		{"group then a literal letter", "Saison 2 Episode 5", `(?i)Saison (\d+) Episode (\d+)`, "S$1E$2", "S2E5"},
		{"braced group number", "Saison 2 Episode 5", `Saison (\d+) Episode (\d+)`, "S${1}E${2}", "S2E5"},
		{"named group", "Saison 2", `Saison (?<season>\d+)`, "S${season}", "S2"},
		{"whole match", "abc", `b`, "[$0]", "a[b]c"},
		{"escaped dollar", "5", `(\d)`, "$$$1", "$5"},
		{"unbraced name stays literal, as in .NET", "x", `(?<n>x)`, "$n", "$n"},
	}
	reReplace := funcMap["re_replace"].(func(string, string, string) (string, error))
	for _, c := range cases {
		t.Run("filter/"+c.name, func(t *testing.T) {
			got, err := Filters["re_replace"](ctx, c.value, []string{c.pattern, c.repl}, tc)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
		t.Run("template/"+c.name, func(t *testing.T) {
			got, err := reReplace(c.value, c.pattern, c.repl)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestRegexFiltersRunTheCorpusLookaroundPatterns runs patterns taken
// verbatim from bundled definitions that RE2 cannot compile, on the kind of
// title each was written for.
func TestRegexFiltersRunTheCorpusLookaroundPatterns(t *testing.T) {
	ctx := context.Background()
	tc := &TemplateContext{Config: map[string]any{"multilanguage": "FRENCH"}}
	// yggtorrent.yml, title_multilang: a negative lookahead, and a
	// templated replacement.
	ygg := []string{`(?i)\b(MULTI(?!.*(?:FRENCH|ENGLISH|VOSTFR)))\b`, "{{ .Config.multilanguage }}"}
	cases := []struct {
		name, filter, value string
		args                []string
		want                string
	}{
		{"yggtorrent: a bare MULTi becomes the configured language", "re_replace", "Le Film 2023 MULTi 1080p WEB", ygg, "Le Film 2023 FRENCH 1080p WEB"},
		{"yggtorrent: a MULTi already naming a language stays", "re_replace", "Le Film 2023 MULTi FRENCH 1080p", ygg, "Le Film 2023 MULTi FRENCH 1080p"},
		// brasiltracker.yml: a negative lookbehind.
		{"brasiltracker: HD becomes 720p", "re_replace", "Filme HD Dublado", []string{`(?i)\b(?<!Full )HD\b`, "720p"}, "Filme 720p Dublado"},
		{"brasiltracker: Full HD stays", "re_replace", "Filme Full HD Dublado", []string{`(?i)\b(?<!Full )HD\b`, "720p"}, "Filme Full HD Dublado"},
		// hdonly.yml: a backreference.
		{"hdonly: a doubled .MULTI collapses", "re_replace", "Film.MULTI.MULTI.1080p", []string{`(\.MULTI)\1`, ".MULTI"}, "Film.MULTI.1080p"},
		// The Ukrainian trackers' "S$1E$2 of $3".
		{"S$1E$2 of $3", "re_replace", "Сезон 2 Серія 5 з 10", []string{`(?i)[CС]езони?[\s:]*(\d+(?:-\d+)?).+?(?:[CС]ері[їяй]|Епізоди?)[\s:]*(\d+(?:-\d+)?)\s*з\s*(\w?)`, "S$1E$2 of $3"}, "S2E5 of 10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Filters[c.filter](ctx, c.value, c.args, tc)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestCatastrophicBacktrackingTimesOut proves a pathological pattern ends
// as the field's error at regexMatchTimeout, rather than hanging the search.
func TestCatastrophicBacktrackingTimesOut(t *testing.T) {
	ctx := context.Background()
	value := strings.Repeat("a", 64) + "!"
	for _, name := range []string{"regexp", "re_replace"} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			_, err := Filters[name](ctx, value, []string{`^(a+)+$`, "x"}, &TemplateContext{})
			require.Error(t, err)
			assert.ErrorIs(t, err, errRegexTimeout)
			assert.NotContains(t, err.Error(), value, "the input, which may carry a passkey, is not quoted")
			assert.Less(t, time.Since(start), regexMatchTimeout+5*time.Second)
		})
	}
	t.Run("template", func(t *testing.T) {
		_, err := render(`{{ re_replace .Keywords "^(a+)+$" "x" }}`, &TemplateContext{Keywords: value})
		require.ErrorIs(t, err, errRegexTimeout)
	})
}

// TestRegexCacheReusesCompiledPatterns holds the compile cache to one
// compile per (pattern, options), concurrency-safe and bounded.
func TestRegexCacheReusesCompiledPatterns(t *testing.T) {
	c := newRegexCache(4)
	a, err := c.compile(`S(\d+)`, 0)
	require.NoError(t, err)
	b, err := c.compile(`S(\d+)`, 0)
	require.NoError(t, err)
	assert.Same(t, a, b, "a second compile of one pattern is served from the cache")

	_, err = c.compile(`(`, 0)
	require.Error(t, err)
	_, err = c.compile(`(`, 0)
	require.Error(t, err, "a compile error is cached too")

	for i := range 10 {
		_, err := c.compile(strings.Repeat("x", i+1), 0)
		require.NoError(t, err)
	}
	assert.LessOrEqual(t, c.len(), 4, "the cache stays bounded")

	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range 200 {
				_, _ = c.compile(strings.Repeat("y", i%7+1), 0)
			}
		}()
	}
	for range 8 {
		<-done
	}
	assert.LessOrEqual(t, c.len(), 4)
}

// TestNamedBlocksRunAsInDotNet runs the Cyrillic-stripping filter 34
// bundled definitions share (uniondht.yml's title, stripcyrillic on), whose
// \p{IsCyrillic} is a .NET named block regexp2 does not know.
func TestNamedBlocksRunAsInDotNet(t *testing.T) {
	ctx := context.Background()
	strip := []string{
		`(\([\p{IsCyrillic}\W]+\))|(^[\p{IsCyrillic}\W\d]+\/ )|([\p{IsCyrillic} \-]+,+)|([\p{IsCyrillic}]+)`,
		"{{ if .Config.stripcyrillic }}{{ else }}$1$2$3$4{{ end }}",
	}
	on := &TemplateContext{Config: map[string]any{"stripcyrillic": "true"}}
	off := &TemplateContext{Config: map[string]any{"stripcyrillic": ""}}
	title := "Дюна: Часть вторая / Dune: Part Two (2024) WEB-DL 1080p"

	got, err := Filters["re_replace"](ctx, title, strip, on)
	require.NoError(t, err)
	assert.Equal(t, "Dune: Part Two (2024) WEB-DL 1080p", got)

	got, err = Filters["re_replace"](ctx, title, strip, off)
	require.NoError(t, err)
	assert.Equal(t, title, got, "with the setting off every group is put back")

	// thepiratebay.yml: CJK runs become dots.
	got, err = Filters["re_replace"](ctx, "沙丘2 Dune Part Two", []string{`([\p{IsCJKUnifiedIdeographs}\W]+)`, "."}, on)
	require.NoError(t, err)
	assert.Equal(t, ".2.Dune.Part.Two", got)
}

func TestExpandNamedBlocks(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no block", `S(\d+)E(\d+)`, `S(\d+)E(\d+)`},
		{"outside a class", `\p{IsCyrillic}+`, `[\u0400-\u04FF]+`},
		{"negated outside a class", `\P{IsCyrillic}`, `[^\u0400-\u04FF]`},
		{"inside a class", `[\p{IsCyrillic}\W]`, `[\u0400-\u04FF\W]`},
		{"negated inside a class", `[\P{IsBasicLatin}]`, `[\u0080-` + string(rune(0x10FFFF)) + `]`},
		{"an escaped backslash is not an escape", `\\p{IsCyrillic}`, `\\p{IsCyrillic}`},
		{"a literal ] first in a class", `[]\p{IsGreek}]`, `[]\u0370-\u03FF]`},
		{"a category is left to regexp2", `\p{Lu}\p{IsGreek}`, `\p{Lu}[\u0370-\u03FF]`},
		{"an unknown block is left for the compiler", `\p{IsKlingon}`, `\p{IsKlingon}`},
		{".NET class subtraction", `[a-z-[\p{IsBasicLatin}]]`, `[a-z-[\u0000-\u007F]]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, expandNamedBlocks(c.in))
		})
	}
	_, err := compileRegex(`\p{IsKlingon}`)
	require.Error(t, err, "a block .NET does not name still fails to compile")

	re, err := compileRegex(`[\P{IsBasicLatin}]+`)
	require.NoError(t, err)
	got, err := regexMatchGroup1(re, "cafe café 🎬")
	require.NoError(t, err)
	assert.Equal(t, "é", got, "the complement reaches past the BMP")
	got, err = regexReplaceAll(re, "a🎬b", "")
	require.NoError(t, err)
	assert.Equal(t, "ab", got)
}
