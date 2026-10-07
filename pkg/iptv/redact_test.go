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

package iptv_test

import (
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/iptv"
)

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"http://user:pw@provider.example/get.php?username=u&password=p&type=m3u_plus": "http://provider.example/get.php",
		"http://provider.example/live/user/pass/1001.ts":                             "http://provider.example/…/1001.ts",
		"http://provider.example:8080/":                                              "http://provider.example:8080/",
		"https://epg.example/guide.xml.gz?token=abc#frag":                            "https://epg.example/guide.xml.gz",
		"not a url\x7f":                                                              "<redacted url>",
		"/relative/path":                                                             "<redacted url>",
	} {
		assert.Equal(t, want, iptv.Redact(in), in)
	}
}

func TestRedactErrorRewritesTheURLInsideAURLError(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "http://provider.example/live/user/pass/1001.ts", Err: errors.New("connection refused")}
	got := iptv.RedactError(fmt.Errorf("fetch playlist: %w", err))
	assert.Equal(t, `fetch playlist: Get "http://provider.example/…/1001.ts": connection refused`, got)
	assert.NotContains(t, got, "pass")
	assert.Equal(t, "plain", iptv.RedactError(errors.New("plain")))
	assert.Equal(t, "", iptv.RedactError(nil))
}
