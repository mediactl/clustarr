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

package naming

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var tokenRe = regexp.MustCompile(`\{([^{}]*)\}`)

func (e Engine) Render(tmpl string, c Context) (string, error) {
	var errOut error
	out := tokenRe.ReplaceAllStringFunc(tmpl, func(raw string) string {
		if errOut != nil {
			return raw
		}
		prefix, name, suffix := splitWrapper(raw[1 : len(raw)-1])
		base, pad, trunc := splitModifier(name)
		fn, ok := tokenFuncs[normalizeTokenName(base)]
		if !ok {
			errOut = fmt.Errorf("%w: %q", ErrUnknownToken, base)
			return raw
		}
		val := fn(c, pad, trunc)
		if val == "" {
			return ""
		}
		return prefix + val + suffix
	})
	if errOut != nil {
		return "", errOut
	}
	return out, nil
}

func splitWrapper(spec string) (prefix, name, suffix string) {
	name = spec
	if len(name) > 0 {
		switch name[0] {
		case '-', '[', '(', ' ':
			prefix, name = string(name[0]), name[1:]
		}
	}
	if len(name) > 0 {
		switch name[len(name)-1] {
		case ']', ')':
			suffix, name = string(name[len(name)-1]), name[:len(name)-1]
		}
	}
	return prefix, name, suffix
}

func splitModifier(name string) (base string, pad, trunc int) {
	i := lastColon(name)
	if i < 0 {
		return name, 0, 0
	}
	mod := name[i+1:]
	if mod != "" && allZero(mod) {
		return name[:i], len(mod), 0
	}
	if n, err := strconv.Atoi(mod); err == nil && n > 0 {
		return name[:i], 0, n
	}
	return name, 0, 0
}

func lastColon(s string) int {
	return strings.LastIndex(s, ":")
}

func allZero(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return true
}

func yearString(y int) string {
	if y == 0 {
		return ""
	}
	return strconv.Itoa(y)
}

func normalizeTokenName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// tokenFuncs is grown by every later step; this step seeds it with the
// three tokens Step 1's test needs.
var tokenFuncs = map[string]func(c Context, pad, trunc int) string{
	"movie title":  func(c Context, _, _ int) string { return c.Title },
	"release year": func(c Context, _, _ int) string { return yearString(c.Year) },
}
