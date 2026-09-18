//go:build tools

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

// Package deps exists only to keep `go mod tidy` from removing modules that
// are pre-added for a later task but not yet imported by real code. Delete an
// entry the moment a real importer lands -- an entry that outlives its task is
// a dependency nobody can account for.
package deps

import (
	_ "github.com/Masterminds/sprig/v3"
	_ "github.com/PuerkitoBio/goquery"
	_ "github.com/antchfx/xmlquery"
	_ "github.com/asticode/go-astisub"
	_ "github.com/cyruzin/golang-tmdb"
	_ "github.com/dlclark/regexp2"
	_ "github.com/gabriel-vasile/mimetype"
	_ "github.com/goccy/go-yaml"
	_ "github.com/hashicorp/golang-lru/v2"
	_ "github.com/lithammer/fuzzysearch/fuzzy"
	_ "github.com/moistari/rls"
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "github.com/tidwall/gjson"
	_ "go.uploadedlobster.com/musicbrainzws2"
	_ "golang.org/x/net/proxy"
	_ "golang.org/x/time/rate"
	_ "gopkg.in/vansante/go-ffprobe.v2"
)
