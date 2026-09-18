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

package cardigann_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

func TestAllTwentyFiveFiltersAreRegistered(t *testing.T) {
	want := []string{
		"querystring", "timeparse", "dateparse", "regexp", "re_replace", "split", "replace", "trim",
		"prepend", "append", "tolower", "toupper", "urldecode", "urlencode", "htmldecode", "htmlencode",
		"timeago", "reltime", "fuzzytime", "validfilename", "diacritics", "jsonjoinarray", "hexdump",
		"strdump", "validate",
	}
	assert.Len(t, want, 25)
	for _, name := range want {
		_, ok := cardigann.Filters[name]
		assert.Truef(t, ok, "filter %q not registered", name)
	}
	assert.Len(t, cardigann.Filters, 25, "no extra, invented filter names")
}

func TestFilters(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	tc := &cardigann.TemplateContext{Now: now}
	cases := []struct {
		name, filter, value string
		args                []string
		want                string
	}{
		{"querystring", "querystring", "https://x/?foo=bar&baz=qux", []string{"foo"}, "bar"},
		{"regexp capture group 1", "regexp", "S01E05 Something", []string{`S(\d+)E(\d+)`}, "01"},
		{"re_replace global", "re_replace", "Foo.Bar.2024", []string{`\.`, " "}, "Foo Bar 2024"},
		{"split positive index", "split", "a/b/c/d", []string{"/", "2"}, "c"},
		{"split negative index", "split", "a/b/c/d", []string{"/", "-1"}, "d"},
		{"replace", "replace", "hello-world", []string{"-", " "}, "hello world"},
		{"trim default whitespace", "trim", "  hello  ", nil, "hello"},
		{"trim cutset", "trim", "xxhelloxx", []string{"x"}, "hello"},
		{"prepend", "prepend", "world", []string{"hello "}, "hello world"},
		{"append", "append", "hello", []string{" world"}, "hello world"},
		{"tolower", "tolower", "HELLO", nil, "hello"},
		{"toupper", "toupper", "hello", nil, "HELLO"},
		{"urldecode", "urldecode", "hello%20world", nil, "hello world"},
		{"urlencode", "urlencode", "hello world", nil, "hello+world"},
		{"htmldecode", "htmldecode", "AT&amp;T", nil, "AT&T"},
		{"htmlencode", "htmlencode", "AT&T", nil, "AT&amp;T"},
		{"validfilename", "validfilename", `My:File*Name?.mkv`, nil, "MyFileName.mkv"},
		{"diacritics", "diacritics", "café", []string{"replace"}, "cafe"},
		{"jsonjoinarray", "jsonjoinarray", `{"genres":["Action","Sci-Fi"]}`, []string{"genres", ", "}, "Action, Sci-Fi"},
		{"hexdump passthrough", "hexdump", "unchanged", nil, "unchanged"},
		{"strdump passthrough", "strdump", "unchanged", nil, "unchanged"},
		{"validate keeps allow-listed", "validate", "Comedy", []string{"Action,Comedy,Drama"}, "Comedy"},
		{"validate drops unlisted", "validate", "Horror", []string{"Action,Comedy,Drama"}, ""},
		{"dateparse to RFC3339", "dateparse", "2024-09-17 10:11:12 +00:00", []string{"yyyy-MM-dd HH:mm:ss zzz"}, "2024-09-17T10:11:12Z"},
		{"timeparse is an alias of dateparse", "timeparse", "2024-09-17 10:11:12 +00:00", []string{"yyyy-MM-dd HH:mm:ss zzz"}, "2024-09-17T10:11:12Z"},
		{"dateparse lower-case am/pm from the 1337x corpus", "dateparse", "7am Sep. 14", []string{"htt MMM. d"}, "2026-09-14T07:00:00Z"},
		{"timeago", "timeago", "2 hours ago", nil, now.Add(-2 * time.Hour).Format(time.RFC3339)},
		{"reltime", "reltime", "3 days", nil, now.Add(-72 * time.Hour).Format(time.RFC3339)},
		{"fuzzytime now", "fuzzytime", "now", nil, now.Format(time.RFC3339)},
		{"fuzzytime today", "fuzzytime", "Today 12:25", nil, time.Date(2026, 9, 18, 12, 25, 0, 0, time.UTC).Format(time.RFC3339)},
		{"fuzzytime yesterday", "fuzzytime", "Yesterday 12:25", nil, time.Date(2026, 9, 17, 12, 25, 0, 0, time.UTC).Format(time.RFC3339)},
	}
	for _, tc2 := range cases {
		t.Run(tc2.name, func(t *testing.T) {
			f, ok := cardigann.Filters[tc2.filter]
			require.True(t, ok)
			got, err := f(context.Background(), tc2.value, tc2.args, tc)
			require.NoError(t, err)
			assert.Equal(t, tc2.want, got)
		})
	}
}

func TestTranslateDateFormat(t *testing.T) {
	cases := map[string]string{
		"yyyy-MM-dd HH:mm:ss zzz": "2006-01-02 15:04:05 -07:00",
		"MMM. d yy":               "Jan. 2 06",
		"htt MMM. d":              "3PM Jan. 2",
		"MM/dd/yyyy HH:mm:ss zzz": "01/02/2006 15:04:05 -07:00",
	}
	for in, want := range cases {
		assert.Equal(t, want, cardigann.TranslateDateFormat(in))
	}
}

func TestGetBytes(t *testing.T) {
	cases := map[string]int64{
		"1.5 GB":      1610612736,
		"700 MiB":     734003200,
		"1,234 KB":    1263616,
		"21474836480": 21474836480, // bare integer, already bytes (0dayfiles' size field)
	}
	for in, want := range cases {
		got, err := cardigann.GetBytes(in)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := cardigann.GetBytes("")
	assert.Error(t, err)
}
