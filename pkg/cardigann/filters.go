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
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Filter is one named transform in the pipeline a SelectorBlock/FilterBlock
// applies to an extracted value. ctx carries the logger for the two
// debug-only filters (hexdump, strdump); tc supplies .Config/.Result for
// filters whose args are themselves templates (re_replace's replacement,
// prepend/append's text).
type Filter func(ctx context.Context, value string, args []string, tc *TemplateContext) (string, error)

// Filters holds exactly the 25 names FilterBlock's schema enum allows — no
// other name is ever added; a filter name outside this set is a schema
// violation.
var Filters = map[string]Filter{
	"querystring":   filterQuerystring,
	"timeparse":     filterDateparse, // identical implementation; only the name differs across the Jackett-derived corpus
	"dateparse":     filterDateparse,
	"regexp":        filterRegexp,
	"re_replace":    filterReReplace,
	"split":         filterSplit,
	"replace":       filterReplace,
	"trim":          filterTrim,
	"prepend":       filterPrepend,
	"append":        filterAppend,
	"tolower":       filterTolower,
	"toupper":       filterToupper,
	"urldecode":     filterURLDecode,
	"urlencode":     filterURLEncode,
	"htmldecode":    filterHTMLDecode,
	"htmlencode":    filterHTMLEncode,
	"timeago":       filterTimeago,
	"reltime":       filterReltime,
	"fuzzytime":     filterFuzzytime,
	"validfilename": filterValidFilename,
	"diacritics":    filterDiacritics,
	"jsonjoinarray": filterJSONJoinArray,
	"hexdump":       filterHexdump,
	"strdump":       filterStrdump,
	"validate":      filterValidate,
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func filterQuerystring(_ context.Context, value string, args []string, _ *TemplateContext) (string, error) {
	u, err := url.Parse(value)
	if err != nil {
		// value is a selector-extracted tracker link -- extracting a
		// passkey out of one is why this filter exists -- and
		// url.Error.Error() embeds its whole input, so the error is
		// reduced to its cause (ruling F6). The link itself is never
		// interpolated here.
		return "", fmt.Errorf("cardigann: querystring: %w", RedactErr(err))
	}
	return u.Query().Get(arg(args, 0)), nil
}

func filterRegexp(_ context.Context, value string, args []string, _ *TemplateContext) (string, error) {
	re, err := regexp.Compile(arg(args, 0))
	if err != nil {
		return "", fmt.Errorf("cardigann: regexp: %w", err)
	}
	m := re.FindStringSubmatch(value)
	switch {
	case len(m) > 1:
		return m[1], nil
	case len(m) == 1:
		return m[0], nil
	default:
		return "", nil
	}
}

func filterReReplace(_ context.Context, value string, args []string, tc *TemplateContext) (string, error) {
	re, err := regexp.Compile(arg(args, 0))
	if err != nil {
		return "", fmt.Errorf("cardigann: re_replace: %w", err)
	}
	repl, err := render(arg(args, 1), tc)
	if err != nil {
		return "", fmt.Errorf("cardigann: re_replace: replacement: %w", err)
	}
	return re.ReplaceAllString(value, repl), nil
}

func filterSplit(_ context.Context, value string, args []string, _ *TemplateContext) (string, error) {
	sep := arg(args, 0)
	parts := strings.Split(value, sep)
	idx, err := strconv.Atoi(arg(args, 1))
	if err != nil {
		return "", fmt.Errorf("cardigann: split: index: %w", err)
	}
	if idx < 0 {
		idx += len(parts)
	}
	if idx < 0 || idx >= len(parts) {
		return "", fmt.Errorf("cardigann: split: index %d out of range for %d part(s)", idx, len(parts))
	}
	return parts[idx], nil
}

func filterReplace(_ context.Context, value string, args []string, _ *TemplateContext) (string, error) {
	return strings.ReplaceAll(value, arg(args, 0), arg(args, 1)), nil
}

func filterTrim(_ context.Context, value string, args []string, _ *TemplateContext) (string, error) {
	if len(args) == 0 || args[0] == "" {
		return strings.TrimSpace(value), nil
	}
	return strings.Trim(value, args[0]), nil
}

func filterPrepend(_ context.Context, value string, args []string, tc *TemplateContext) (string, error) {
	text, err := render(arg(args, 0), tc)
	if err != nil {
		return "", fmt.Errorf("cardigann: prepend: %w", err)
	}
	return text + value, nil
}

func filterAppend(_ context.Context, value string, args []string, tc *TemplateContext) (string, error) {
	text, err := render(arg(args, 0), tc)
	if err != nil {
		return "", fmt.Errorf("cardigann: append: %w", err)
	}
	return value + text, nil
}

func filterTolower(_ context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	return strings.ToLower(value), nil
}

func filterToupper(_ context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	return strings.ToUpper(value), nil
}

func filterURLDecode(_ context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	v, err := url.QueryUnescape(value)
	if err != nil {
		return "", fmt.Errorf("cardigann: urldecode: %w", err)
	}
	return v, nil
}

func filterURLEncode(_ context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	return url.QueryEscape(value), nil
}

func filterHTMLDecode(_ context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	return html.UnescapeString(value), nil
}

func filterHTMLEncode(_ context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	return html.EscapeString(value), nil
}

// validFilenameChars is the Windows-reserved set Prowlarr's own filter
// targets: <>:"/\|?*
const validFilenameChars = `<>:"/\|?*`

func filterValidFilename(_ context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(validFilenameChars, r) {
			return -1
		}
		return r
	}, value), nil
}

// diacriticsTable is a hand-rolled ASCII transliteration table sized to the
// corpus's actual accented characters, used instead of golang.org/x/text/
// unicode/norm: verified against this task's allowed-imports list,
// golang.org/x/text is not in it (only golang.org/x/net is), so this filter
// stays dependency-free rather than importing a module the brief did not
// pre-approve.
var diacriticsTable = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'ā': 'a',
	'À': 'A', 'Á': 'A', 'Â': 'A', 'Ã': 'A', 'Ä': 'A', 'Å': 'A', 'Ā': 'A',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e',
	'È': 'E', 'É': 'E', 'Ê': 'E', 'Ë': 'E', 'Ē': 'E',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i',
	'Ì': 'I', 'Í': 'I', 'Î': 'I', 'Ï': 'I', 'Ī': 'I',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ō': 'o',
	'Ò': 'O', 'Ó': 'O', 'Ô': 'O', 'Õ': 'O', 'Ö': 'O', 'Ō': 'O',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ū': 'u',
	'Ù': 'U', 'Ú': 'U', 'Û': 'U', 'Ü': 'U', 'Ū': 'U',
	'ý': 'y', 'ÿ': 'y', 'Ý': 'Y',
	'ñ': 'n', 'Ñ': 'N',
	'ç': 'c', 'Ç': 'C',
	'ß': 's',
}

func filterDiacritics(_ context.Context, value string, args []string, _ *TemplateContext) (string, error) {
	if mode := arg(args, 0); mode != "" && mode != "replace" {
		return "", fmt.Errorf("cardigann: diacritics: unsupported mode %q", mode)
	}
	return strings.Map(func(r rune) rune {
		if repl, ok := diacriticsTable[r]; ok {
			return repl
		}
		return r
	}, value), nil
}

func filterJSONJoinArray(_ context.Context, value string, args []string, _ *TemplateContext) (string, error) {
	path, sep := arg(args, 0), arg(args, 1)
	r := gjson.Get(value, path)
	if !r.IsArray() {
		return "", fmt.Errorf("cardigann: jsonjoinarray: %q is not an array", path)
	}
	var parts []string
	r.ForEach(func(_, v gjson.Result) bool { parts = append(parts, v.String()); return true })
	return strings.Join(parts, sep), nil
}

func filterHexdump(ctx context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	logging.FromContext(ctx).Debug("cardigann filter", "filter", "hexdump", "value", value)
	return value, nil
}

func filterStrdump(ctx context.Context, value string, _ []string, _ *TemplateContext) (string, error) {
	logging.FromContext(ctx).Debug("cardigann filter", "filter", "strdump", "value", value)
	return value, nil
}

func filterValidate(_ context.Context, value string, args []string, _ *TemplateContext) (string, error) {
	for _, allowed := range strings.Split(arg(args, 0), ",") {
		if value == allowed {
			return value, nil
		}
	}
	return "", nil
}

// parseRelativeDurationRe matches Jackett/Cardigann's "timeago"/"reltime"
// style relative durations: "<n> <unit>[s] [ago]".
var parseRelativeDurationRe = regexp.MustCompile(`(?i)^\s*(\d+)\s*(second|sec|minute|min|hour|hr|day|week|month|year)s?\s*(ago)?\s*$`)

func parseRelativeDuration(value string) (time.Duration, error) {
	m := parseRelativeDurationRe.FindStringSubmatch(value)
	if m == nil {
		return 0, fmt.Errorf("cardigann: cannot parse relative duration %q", value)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("cardigann: relative duration: %w", err)
	}
	switch strings.ToLower(m[2]) {
	case "second", "sec":
		return time.Duration(n) * time.Second, nil
	case "minute", "min":
		return time.Duration(n) * time.Minute, nil
	case "hour", "hr":
		return time.Duration(n) * time.Hour, nil
	case "day":
		return time.Duration(n) * 24 * time.Hour, nil
	case "week":
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case "month":
		return time.Duration(n) * 30 * 24 * time.Hour, nil
	case "year":
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("cardigann: unknown relative duration unit %q", m[2])
	}
}

func filterTimeago(_ context.Context, value string, _ []string, tc *TemplateContext) (string, error) {
	d, err := parseRelativeDuration(value)
	if err != nil {
		return "", fmt.Errorf("cardigann: timeago: %w", err)
	}
	return tc.effectiveNow().Add(-d).Format(time.RFC3339), nil
}

func filterReltime(_ context.Context, value string, _ []string, tc *TemplateContext) (string, error) {
	d, err := parseRelativeDuration(value)
	if err != nil {
		return "", fmt.Errorf("cardigann: reltime: %w", err)
	}
	return tc.effectiveNow().Add(-d).Format(time.RFC3339), nil
}

// clockLayouts are tried in order to parse a bare time-of-day, covering
// both the 24-hour "Today 12:25"/"Yesterday 12:25" form this filter's own
// schema examples use and the 12-hour am/pm form the 1337x corpus's bare
// "12:25am" (no Today/Yesterday prefix) uses.
var clockLayouts = []string{"15:04", "3:04pm", "3:04 pm", "3:04PM", "3:04 PM"}

func combineDateAndClock(day time.Time, clock string) (time.Time, error) {
	clock = strings.TrimSpace(clock)
	var t time.Time
	var err error
	for _, layout := range clockLayouts {
		t, err = time.Parse(layout, clock)
		if err == nil {
			return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), t.Second(), 0, day.Location()), nil
		}
	}
	return time.Time{}, fmt.Errorf("cardigann: fuzzytime: cannot parse clock %q: %w", clock, err)
}

func filterFuzzytime(_ context.Context, value string, _ []string, tc *TemplateContext) (string, error) {
	now := tc.effectiveNow()
	v := strings.TrimSpace(value)
	lower := strings.ToLower(v)

	switch {
	case lower == "now":
		return now.Format(time.RFC3339), nil
	case strings.HasPrefix(lower, "today"):
		t, err := combineDateAndClock(now, v[len("today"):])
		if err != nil {
			return "", err
		}
		return t.Format(time.RFC3339), nil
	case strings.HasPrefix(lower, "yesterday"):
		t, err := combineDateAndClock(now.AddDate(0, 0, -1), v[len("yesterday"):])
		if err != nil {
			return "", err
		}
		return t.Format(time.RFC3339), nil
	default:
		t, err := combineDateAndClock(now, v)
		if err != nil {
			return "", err
		}
		return t.Format(time.RFC3339), nil
	}
}

// TranslateDateFormat converts a .NET custom date/time format (dateparse's
// and timeparse's arg, e.g. "yyyy-MM-dd HH:mm:ss zzz") into a Go reference
// layout. Tokens already in Go form pass through unchanged — the corpus
// mixes both dialects (note §3.6).
func TranslateDateFormat(dotnet string) string {
	var b strings.Builder
	runes := []rune(dotnet)
	for i := 0; i < len(runes); {
		c := runes[i]
		j := i
		for j < len(runes) && runes[j] == c {
			j++
		}
		length := j - i
		switch c {
		case 'y':
			switch length {
			case 2:
				b.WriteString("06")
			default: // length >= 4, or the rare bare "y"
				b.WriteString("2006")
			}
		case 'M':
			switch {
			case length >= 3:
				b.WriteString("Jan")
			case length == 2:
				b.WriteString("01")
			default:
				b.WriteString("1")
			}
		case 'd':
			if length >= 2 {
				b.WriteString("02")
			} else {
				b.WriteString("2")
			}
		case 'H':
			b.WriteString("15")
		case 'h':
			if length >= 2 {
				b.WriteString("03")
			} else {
				b.WriteString("3")
			}
		case 'm':
			if length >= 2 {
				b.WriteString("04")
			} else {
				b.WriteString("4")
			}
		case 's':
			if length >= 2 {
				b.WriteString("05")
			} else {
				b.WriteString("5")
			}
		case 't':
			b.WriteString("PM")
		case 'z':
			switch {
			case length >= 3:
				b.WriteString("-07:00")
			case length == 2:
				b.WriteString("-07")
			default:
				b.WriteString("-0700")
			}
		default:
			b.WriteString(string(runes[i:j]))
		}
		i = j
	}
	return b.String()
}

// parseWithLayout parses value against dotnetFormat, retrying against
// strings.ToUpper(value) when the translated layout contains "PM": Go's
// "PM" layout token only matches the literal upper-case "AM"/"PM" in the
// source text, but the corpus (1337x's "htt MMM. d" dates) writes
// lower-case ("7am"), so a naive translation silently fails to parse every
// htt-formatted date without this retry.
func parseWithLayout(dotnetFormat, value string) (time.Time, error) {
	layout := TranslateDateFormat(dotnetFormat)
	t, err := time.Parse(layout, value)
	if err != nil && strings.Contains(layout, "PM") {
		t, err = time.Parse(layout, strings.ToUpper(value))
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("cardigann: parse date %q with layout %q: %w", value, layout, err)
	}
	return t, nil
}

func filterDateparse(_ context.Context, value string, args []string, tc *TemplateContext) (string, error) {
	dotnetFormat := arg(args, 0)
	t, err := parseWithLayout(dotnetFormat, value)
	if err != nil {
		return "", err
	}
	// A format with no year token ("htt MMM. d", 1337x's "this year"
	// dates) parses to Go's zero year 0000; fill in the current year
	// (tc.effectiveNow(), deterministic under test) instead, matching
	// what the source page actually means by an undated "7am Sep. 14th".
	if !strings.ContainsRune(dotnetFormat, 'y') {
		now := tc.effectiveNow()
		t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	}
	return t.Format(time.RFC3339), nil
}

// byteUnits are the human size units GetBytes accepts. Non-"i" units are
// treated as binary (1 GB == 2^30), matching Jackett/Prowlarr's
// ParseUtil.GetBytes and how trackers actually author their pages.
var byteUnits = map[string]float64{
	"b":  1,
	"kb": 1 << 10, "kib": 1 << 10,
	"mb": 1 << 20, "mib": 1 << 20,
	"gb": 1 << 30, "gib": 1 << 30,
	"tb": 1 << 40, "tib": 1 << 40,
	"pb": 1 << 50, "pib": 1 << 50,
}

var byteSizeRe = regexp.MustCompile(`(?i)^([\d.]+)\s*([a-z]*)$`)

// GetBytes parses a human size string ("1.5 GB", "700 MiB", "1,234 KB", or
// a bare integer already in bytes) into a byte count, treating the plain
// (non-"i") units as binary (1 GB == 2^30), matching Jackett/Prowlarr's
// ParseUtil.GetBytes and how trackers actually author their pages.
func GetBytes(s string) (int64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0, fmt.Errorf("cardigann: GetBytes: empty input")
	}
	m := byteSizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("cardigann: GetBytes: cannot parse %q", s)
	}
	num, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("cardigann: GetBytes: %w", err)
	}
	unit := strings.ToLower(m[2])
	if unit == "" {
		return int64(num), nil
	}
	mult, ok := byteUnits[unit]
	if !ok {
		return 0, fmt.Errorf("cardigann: GetBytes: unknown unit %q", unit)
	}
	return int64(num * mult), nil
}
