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
	"container/list"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
)

// A definition's regexes are .NET regexes: Prowlarr's CardigannBase
// compiles the regexp and re_replace filters and the re_replace template
// function each with a bare `new Regex(pattern)` -- RegexOptions.None, no
// timeout -- and calls Match(...).Groups[1] or Replace(input, replacement).
// So they run here on regexp2 with regexp2.None: .NET syntax (lookaround,
// backreferences, balancing groups, which Go's RE2 rejects -- 135 filter
// patterns in 106 bundled definitions) and .NET substitutions, where "$1E"
// is group 1 then a literal E, not Go's group named "1E" (283 replacements
// in 55 definitions read "S$1E$2"). IgnoreCase only where a pattern says
// (?i), as in Prowlarr. .NET's named blocks (\p{IsCyrillic}), which regexp2
// lacks, are rewritten to ranges first (regexblocks.go).

// definitionRegexOptions is the RegexOptions Prowlarr compiles every
// definition regex with.
const definitionRegexOptions = regexp2.None

// regexMatchTimeout bounds one match or replace. Prowlarr sets none, but a
// definition's pattern runs against tracker-controlled text, and a
// preprocessing filter against the whole response body (up to the engine's
// body cap), so a backtracking pattern must end as the field's error rather
// than hang a search. One second is far above what any bundled pattern
// takes on a full results page and well inside a search's deadline.
const regexMatchTimeout = time.Second

// regexCacheSize bounds the compile cache: the bundled corpus holds about
// 2,700 distinct regex uses, but one indexarr replica runs the handful of
// definitions its Indexers name.
const regexCacheSize = 512

// errRegexTimeout is what a match that ran past regexMatchTimeout returns.
// regexp2's own timeout error quotes the whole input, which may be a
// passkey-bearing link or a page body, so it is never passed on.
var errRegexTimeout = errors.New("regex match timeout")

// regexes is the process-wide compile cache every definition regex goes
// through, so a field's pattern compiles once rather than once per row.
var regexes = newRegexCache(regexCacheSize)

// compileRegex compiles a definition's pattern as Prowlarr does, with
// regexMatchTimeout set, through the shared cache.
func compileRegex(pattern string) (*regexp2.Regexp, error) {
	return regexes.compile(pattern, definitionRegexOptions)
}

// regexMatchGroup1 is the regexp filter's match: capture group 1, or the
// whole match for a pattern with no group, or "" for no match.
func regexMatchGroup1(re *regexp2.Regexp, value string) (string, error) {
	m, err := re.FindStringMatch(value)
	if err != nil {
		return "", redactRegexErr(err)
	}
	if m == nil {
		return "", nil
	}
	if g := m.GroupByNumber(1); g != nil {
		return g.String(), nil
	}
	return m.String(), nil
}

// regexReplaceAll replaces every match of re in value, reading repl with
// .NET's substitution grammar ($1, ${name}, $0, $$).
func regexReplaceAll(re *regexp2.Regexp, value, repl string) (string, error) {
	out, err := re.Replace(value, repl, -1, -1)
	if err != nil {
		return "", redactRegexErr(err)
	}
	return out, nil
}

// redactRegexErr turns regexp2's timeout error, which quotes the input,
// into errRegexTimeout. regexp2 v1.12.0 has no typed timeout error, so it
// is recognised by its text (as pkg/release's isRegexTimeout does).
func redactRegexErr(err error) error {
	if strings.HasPrefix(err.Error(), "match timeout") {
		return fmt.Errorf("%w after %v", errRegexTimeout, regexMatchTimeout)
	}
	return err
}

type regexCacheKey struct {
	pattern string
	opts    regexp2.RegexOptions
}

type regexCacheEntry struct {
	key regexCacheKey
	re  *regexp2.Regexp
	err error
}

// regexCache is a bounded, least-recently-used cache of compiled regexes,
// safe for concurrent use. A compile error is cached too, so a pattern that
// does not compile fails each row without recompiling. A *regexp2.Regexp
// is safe for concurrent matching, and its MatchTimeout is set before it is
// shared.
type regexCache struct {
	mu      sync.Mutex
	max     int
	entries map[regexCacheKey]*list.Element
	order   *list.List // front is most recently used
}

func newRegexCache(maxEntries int) *regexCache {
	return &regexCache{max: maxEntries, entries: map[regexCacheKey]*list.Element{}, order: list.New()}
}

func (c *regexCache) compile(pattern string, opts regexp2.RegexOptions) (*regexp2.Regexp, error) {
	key := regexCacheKey{pattern: pattern, opts: opts}
	c.mu.Lock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		e := el.Value.(*regexCacheEntry)
		c.mu.Unlock()
		return e.re, e.err
	}
	c.mu.Unlock()

	// Compile outside the lock; two callers racing on one new pattern
	// both compile, and the first to store wins.
	re, err := regexp2.Compile(expandNamedBlocks(pattern), opts)
	if err == nil {
		re.MatchTimeout = regexMatchTimeout
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		e := el.Value.(*regexCacheEntry)
		return e.re, e.err
	}
	c.entries[key] = c.order.PushFront(&regexCacheEntry{key: key, re: re, err: err})
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*regexCacheEntry).key)
	}
	return re, err
}

func (c *regexCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
