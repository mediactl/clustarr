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

// One test per Cardigann v11 field that was decoded but never read until
// Task X8a: each builds a small definition of its own (through Load, so the
// schema admits it), serves it from httptest, and asserts the behaviour
// Prowlarr gives the field. Each test keeps its definition small and
// self-contained rather than borrowing a corpus file, so it pins one field's
// behaviour and nothing else (the corpus itself is loaded, whole, by
// indexarr/bundle/embedded's tests).

import (
	"context"
	"crypto/sha1" //nolint:gosec // the fingerprint format under test.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/charmap"

	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// defHeader is every fixture definition's required root, parameterised on
// the encoding. Categories: tracker id 1 is Movies ("Films"), 2 is TV
// ("Series"), 3 is Movies/HD ("HD").
const defHeader = `id: feature
name: feature
description: feature fixture
language: en-US
type: public
encoding: %s
links:
  - https://tracker.example/
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: "Films"}
    - {id: 2, cat: TV, desc: "Series"}
    - {id: 3, cat: Movies/HD, desc: "HD"}
  modes:
    search: [q]
`

// loadFeature loads defHeader (UTF-8) followed by body.
func loadFeature(t *testing.T, body string) *cardigann.Definition {
	t.Helper()
	return loadFeatureEnc(t, "UTF-8", body)
}

func loadFeatureEnc(t *testing.T, enc, body string) *cardigann.Definition {
	t.Helper()
	def, err := cardigann.Load([]byte(fmt.Sprintf(defHeader, enc) + body))
	require.NoError(t, err)
	return def
}

// search runs def against srv with a fixed clock and no settings.
func search(t *testing.T, srv *httptest.Server, def *cardigann.Definition, q string) ([]torznab.Release, error) {
	t.Helper()
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{})
	require.NoError(t, err)
	eng := cardigann.Engine{HTTP: srv.Client(), Now: func() time.Time { return testClock }}
	return eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search", Q: q})
}

// htmlRowFields is a minimal fields block for `<tr><td class=t>` rows.
const htmlRowFields = `  fields:
    title:
      selector: td.t
    size:
      text: 1
    seeders:
      text: 1
    category:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`

func titles(rels []torznab.Release) []string {
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		out = append(out, r.Title)
	}
	return out
}

func TestSearchPreprocessingFiltersRewriteTheBodyBeforeParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<table><tr class="broken-row"><td class="t">A</td></tr><tr class="broken-row"><td class="t">B</td></tr></table>`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  path: search
  preprocessingfilters:
    - name: re_replace
      args: ["broken-row", "row"]
  rows:
    selector: tr.row
`+htmlRowFields)
	rels, err := search(t, srv, def, "x")
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B"}, titles(rels), "rows.selector matches only after the preprocessing filter ran")
}

func TestSearchNoResultsMessageIsZeroResultsNotAParseFailure(t *testing.T) {
	cases := []struct {
		name, message, body string
	}{
		{"message in a plain-text body", `noResultsMessage: "No Torrents Found"`, "Error: No Torrents Found for this query"},
		{"empty message and a blank body", `noResultsMessage: ""`, "  \n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			body := `search:
  paths:
    - path: api
      response:
        type: json
        %s
  rows:
    selector: data
  fields:
    title:
      selector: name
    size:
      selector: size
    seeders:
      selector: seeders
    category:
      text: 1
    download:
      selector: link
`
			rels, err := search(t, srv, loadFeature(t, fmt.Sprintf(body, tc.message)), "x")
			require.NoError(t, err)
			assert.Empty(t, rels)

			// Without the message the same body is not JSON, and says so.
			_, err = search(t, srv, loadFeature(t, fmt.Sprintf(body, "")), "x")
			require.Error(t, err)
		})
	}
}

func TestEncodingAppliesToRequestsResponsesAndURLFilters(t *testing.T) {
	var rawQuery string
	page, err := charmap.Windows1251.NewEncoder().String(`<table><tr><td class="t">Фильм</td></tr></table>`)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, page)
	}))
	defer srv.Close()

	def := loadFeatureEnc(t, "windows-1251", `search:
  path: search
  inputs:
    q: "{{ .Keywords }}"
  rows:
    selector: tr
  fields:
    title:
      selector: td.t
    description:
      text: "аб"
      filters:
        - name: urlencode
    size:
      text: 1
    seeders:
      text: 1
    category:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`)
	rels, err := search(t, srv, def, "аб")
	require.NoError(t, err)
	assert.Equal(t, "q=%E0%E1", rawQuery, "the query string is percent-encoded in windows-1251, not UTF-8")
	require.Len(t, rels, 1)
	assert.Equal(t, "Фильм", rels[0].Title, "the page is decoded from windows-1251")
	assert.Equal(t, "%E0%E1", rels[0].Description, "urlencode encodes in the definition's charset")
}

func TestLoadRefusesAnEncodingOrCertificateTheEngineCannotHonour(t *testing.T) {
	_, err := cardigann.Load([]byte(fmt.Sprintf(defHeader, "klingon-8") + `search:
  path: search
  rows:
    selector: tr
` + htmlRowFields))
	require.Error(t, err)
	assert.ErrorIs(t, err, cardigann.ErrInvalidDefinition)

	_, err = cardigann.Load([]byte(fmt.Sprintf(defHeader, "UTF-8") + `certificates:
  - not-a-fingerprint
search:
  path: search
  rows:
    selector: tr
` + htmlRowFields))
	require.Error(t, err)
	assert.ErrorIs(t, err, cardigann.ErrInvalidDefinition)
}

func TestSearchRedirectIsAnErrorUnlessThePathFollowsIt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login.php?returnto=%2Fsearch&passkey=SECRET", http.StatusFound)
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/results", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/results", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<table><tr><td class="t">A</td></tr></table>`)
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `<html><body>Internal error</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	def := func(path, follow string) *cardigann.Definition {
		return loadFeature(t, fmt.Sprintf(`search:
  paths:
    - path: %s
      followredirect: %s
  rows:
    selector: tr
`, path, follow)+htmlRowFields)
	}

	_, err := search(t, srv, def("search", "false"), "x")
	require.Error(t, err)
	assert.ErrorIs(t, err, cardigann.ErrSessionExpired, "a redirect to the login page is a dead session")
	assert.ErrorIs(t, err, cardigann.ErrRedirected)
	var rerr *cardigann.RedirectError
	require.ErrorAs(t, err, &rerr)
	assert.NotContains(t, err.Error(), "SECRET", "the redirect target is redacted")

	_, err = search(t, srv, def("moved", "false"), "x")
	require.ErrorIs(t, err, cardigann.ErrRedirected)
	assert.NotErrorIs(t, err, cardigann.ErrSessionExpired)

	rels, err := search(t, srv, def("moved", "true"), "x")
	require.NoError(t, err)
	assert.Equal(t, []string{"A"}, titles(rels))

	_, err = search(t, srv, def("broken", "false"), "x")
	var serr *cardigann.StatusError
	require.ErrorAs(t, err, &serr, "a 500 page is not a search with no results")
	assert.Equal(t, http.StatusInternalServerError, serr.StatusCode)
}

// loginFormPage is a form login's landing page: a decoy form, then the real
// one with a hidden field, an unchecked and a checked checkbox, a disabled
// input, and two tokens outside the form for selectorinputs and
// getselectorinputs.
const loginFormPage = `<html><body>
<form id="search" action="/wrong"><input name="q"></form>
<form id="login" action="/do-login?step=1" method="post">
  <input type="hidden" name="returnto" value="/index.php">
  <input type="checkbox" name="remember" value="yes">
  <input type="checkbox" name="keep" value="1" checked>
  <input name="frozen" value="z" disabled>
  <input id="user-field" name="uname" type="text">
  <input name="pass" type="password">
</form>
<span id="gettoken">gtok</span><span id="posttoken">ptok</span>
</body></html>`

const loginFormDefinition = `settings:
  - {name: username, type: text}
  - {name: password, type: password}
login:
  method: form
  path: login
  form: "#login"
  selectors: true
  inputs:
    "#user-field": "{{ .Config.username }}"
    "input[type=password]": "{{ .Config.password }}"
  selectorinputs:
    csrf:
      selector: "#posttoken"
  getselectorinputs:
    gtoken:
      selector: "#gettoken"
  error:
    - selector: div.error
search:
  path: search
  rows:
    selector: tr
` + htmlRowFields

func TestLoginFormSubmitsTheWholeFormAndKeepsEveryCookie(t *testing.T) {
	var got struct {
		query, referer string
		form           map[string][]string
		landingCookie  bool
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "landing", Value: "L", Path: "/"})
		_, _ = io.WriteString(w, loginFormPage)
	})
	mux.HandleFunc("POST /do-login", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got.query, got.referer, got.form = r.URL.RawQuery, r.Referer(), r.PostForm
		c, err := r.Cookie("landing")
		got.landingCookie = err == nil && c.Value == "L"
		// The classic shape: the session cookie rides the redirect.
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "S", Path: "/"})
		http.Redirect(w, r, "/index.php", http.StatusFound)
	})
	mux.HandleFunc("GET /index.php", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "uid", Value: "U", Path: "/"})
		_, _ = io.WriteString(w, "<html>welcome</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	def := loadFeature(t, loginFormDefinition)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"username": "user", "password": "pw"})
	require.NoError(t, err)
	sess, err := cardigann.Engine{HTTP: srv.Client()}.Login(context.Background(), def, cfg)
	require.NoError(t, err)

	assert.Equal(t, "step=1&gtoken=gtok", got.query, "form action kept, getselectorinputs appended to it")
	assert.Equal(t, srv.URL+"/login", got.referer)
	assert.True(t, got.landingCookie, "the landing page's cookie goes back with the submit")
	assert.Equal(t, map[string][]string{
		"returnto": {"/index.php"}, // hidden input carried from the form
		"keep":     {"1"},          // checked checkbox; the unchecked one and the disabled input are absent
		"uname":    {"user"},       // login.inputs keyed by selector (login.selectors)
		"pass":     {"pw"},
		"csrf":     {"ptok"}, // selectorinputs
	}, got.form)

	cookies := map[string]string{}
	for _, c := range sess.Cookies {
		cookies[c.Name] = c.Value
	}
	assert.Equal(t, map[string]string{"landing": "L", "session": "S", "uid": "U"}, cookies,
		"the session keeps the cookie set on the redirect, not only the final page's")
}

func TestLoginLandingFollowsRedirectsOnlyWhenTheDefinitionSaysSo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/real-login", http.StatusFound)
	})
	mux.HandleFunc("GET /real-login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, loginFormPage)
	})
	mux.HandleFunc("POST /do-login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>ok</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	login := func(follow string) error {
		def := loadFeature(t, "followredirect: "+follow+"\n"+loginFormDefinition)
		cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"username": "u", "password": "p"})
		require.NoError(t, err)
		_, err = cardigann.Engine{HTTP: srv.Client()}.Login(context.Background(), def, cfg)
		return err
	}
	require.NoError(t, login("true"))
	err := login("false")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no form matches")
}

func TestCertificatesTrustAPinnedLeafForTheSiteHostOnly(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<table><tr><td class="t">A</td></tr></table>`)
	}))
	defer srv.Close()
	sum := sha1.Sum(srv.Certificate().Raw) //nolint:gosec // the fingerprint format under test.
	pinned := hex.EncodeToString(sum[:])

	// A client that does NOT trust httptest's self-signed certificate,
	// exactly as a production client does not trust an expired one.
	untrusting := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	run := func(certs, site string) error {
		body := `search:
  path: search
  rows:
    selector: tr
` + htmlRowFields
		if certs != "" {
			body = "certificates:\n  - " + certs + "\n" + body
		}
		def := loadFeature(t, body)
		cfg, err := cardigann.NewConfig(def, site, map[string]string{})
		require.NoError(t, err)
		rels, err := cardigann.Engine{HTTP: untrusting}.Search(context.Background(), def, cfg, cardigann.Query{Q: "x"})
		if err == nil && len(rels) != 1 {
			return errors.New("expected one release")
		}
		return err
	}

	require.Error(t, run("", srv.URL+"/"), "no pin: the self-signed certificate is refused")
	require.NoError(t, run(pinned, srv.URL+"/"), "pinned: accepted despite the failed chain")
	require.Error(t, run(strings.Repeat("ab", 20), srv.URL+"/"), "a different fingerprint is refused")

	// The same certificate, reached under another name, is not the site.
	other := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	def := loadFeature(t, "certificates:\n  - "+pinned+"\n"+`search:
  path: `+other+`/search
  rows:
    selector: tr
`+htmlRowFields)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{})
	require.NoError(t, err)
	_, err = cardigann.Engine{HTTP: untrusting}.Search(context.Background(), def, cfg, cardigann.Query{Q: "x"})
	require.Error(t, err, "the pin covers the site host, not every host that presents the certificate")
}

func TestDownloadTestLinkTorrentSkipsASelectorThatServesAWebPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/details", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<a class="dl1" href="/limit">dl</a><a class="dl2" href="/file.torrent">dl</a>`)
	})
	mux.HandleFunc("/limit", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>download limit reached</html>")
	})
	mux.HandleFunc("/file.torrent", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, fakeTorrent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	download := func(extra string) string {
		def := loadFeature(t, extra+`download:
  selectors:
    - selector: a.dl1
      attribute: href
    - selector: a.dl2
      attribute: href
search:
  path: search
  rows:
    selector: tr
`+htmlRowFields)
		cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{})
		require.NoError(t, err)
		rc, err := cardigann.Engine{HTTP: srv.Client()}.Download(context.Background(), def, cfg, "details")
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, fakeTorrent, download(""), "testlinktorrent defaults on: the web page is skipped")
	assert.Contains(t, download("testlinktorrent: false\n"), "download limit reached", "off: the first match is returned as-is")
}

func TestDownloadSendsDownloadHeadersElseSearchHeaders(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, fakeTorrent)
	}))
	defer srv.Close()

	def := loadFeature(t, `settings:
  - {name: apikey, type: text}
search:
  path: search
  headers:
    Authorization: ["Bearer {{ .Config.apikey }}"]
  rows:
    selector: tr
`+htmlRowFields)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"apikey": "K"})
	require.NoError(t, err)
	rc, err := cardigann.Engine{HTTP: srv.Client()}.Download(context.Background(), def, cfg, "file.torrent")
	require.NoError(t, err)
	_ = rc.Close()
	assert.Equal(t, "Bearer K", auth)
}

func TestNewConfigMovesALegacyLinkToTheCurrentOne(t *testing.T) {
	def := loadFeature(t, `legacylinks:
  - https://old.example/
search:
  path: search
  rows:
    selector: tr
`+htmlRowFields)

	cfg, err := cardigann.NewConfig(def, "https://OLD.example", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, "https://tracker.example/", cfg.BaseURL)
	assert.Equal(t, "https://tracker.example/", cfg.Values["sitelink"])

	cfg, err = cardigann.NewConfig(def, "https://mirror.example/", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, "https://mirror.example/", cfg.BaseURL, "a mirror that is not a legacy link is kept")
}

func TestSearchRowsMultipleExpandsEachTorrentAndReadsTheParentWithDotDot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"count":2,"movies":[
			{"title":"Movie A","torrents":[{"quality":"720p","hash":"aaa","size":1},{"quality":"1080p","hash":"bbb","size":2}]},
			{"title":"Movie B","torrents":[{"quality":"720p","hash":"ccc","size":3}]}]}}`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  paths:
    - path: api
      response:
        type: json
  rows:
    selector: data.movies
    attribute: torrents
    multiple: true
    count:
      selector: data.count
  fields:
    _quality:
      selector: quality
    title:
      selector: ..title
      filters:
        - name: append
          args: " {{ .Result._quality }}"
    infohash:
      selector: hash
    size:
      selector: size
    seeders:
      text: 1
    category:
      text: 1
`)
	rels, err := search(t, srv, def, "x")
	require.NoError(t, err)
	assert.Equal(t, []string{"Movie A 720p", "Movie A 1080p", "Movie B 720p"}, titles(rels))
	assert.Equal(t, "bbb", rels[1].InfoHash)
}

func TestSearchRowsAfterMergesFollowingRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<table>
<tr class="r"><td class="t">A</td></tr><tr class="r"><td class="s">100</td></tr>
<tr class="r"><td class="t">B</td></tr><tr class="r"><td class="s">200</td></tr>
<tr class="r"><td class="t">C</td></tr>
</table>`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  path: search
  rows:
    selector: tr.r
    after: 1
  fields:
    title:
      selector: td.t
    size:
      selector: td.s
      optional: true
      default: 0
    seeders:
      text: 1
    category:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`)
	rels, err := search(t, srv, def, "x")
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B", "C"}, titles(rels), "each torrent's second row merged into its first")
	assert.EqualValues(t, 100, rels[0].Size)
	assert.EqualValues(t, 200, rels[1].Size)
	assert.EqualValues(t, 0, rels[2].Size, "a last row with no follower is kept, not an index-out-of-range")
}

func TestSearchDateHeadersDateARowFromTheNearestHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<table>
<tr class="orphan"><td class="t">Orphan</td></tr>
<tr class="day"><td>2026-09-01</td></tr>
<tr class="r"><td class="t">A</td></tr>
<tr class="r"><td class="t">B</td><td class="d">2026-09-17T10:00:00Z</td></tr>
<tr class="day"><td>2026-09-02</td></tr>
<tr class="r"><td class="t">C</td></tr>
</table>`)
	}))
	defer srv.Close()

	// The schema admits `optional: true` or no optional at all.
	def := func(optional string) *cardigann.Definition {
		return loadFeature(t, fmt.Sprintf(`search:
  path: search
  rows:
    selector: tr.r, tr.orphan
    dateheaders:
      selector: tr.day
%s
  fields:
    title:
      selector: td.t
    date:
      selector: td.d
      optional: true
    size:
      text: 1
    seeders:
      text: 1
    category:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`, optional))
	}
	rels, err := search(t, srv, def(""), "x")
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B", "C"}, titles(rels), "a row with no header before it is dropped when dateheaders is required")
	assert.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), rels[0].PubDate.UTC(), "the header row itself matches the selector")
	assert.Equal(t, time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC), rels[1].PubDate.UTC(), "a row's own date wins")
	assert.Equal(t, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), rels[2].PubDate.UTC(), "the nearest header, not the first")

	rels, err = search(t, srv, def("      optional: true"), "x")
	require.NoError(t, err)
	assert.Equal(t, []string{"Orphan", "A", "B", "C"}, titles(rels), "optional keeps the undated row")
}

func TestSearchFieldAppendModifiers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<table><tr><td class="t">Show</td><td class="e">S01E01</td><td class="c">2</td><td class="h">HD</td></tr></table>`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  path: search
  rows:
    selector: tr
  fields:
    title:
      selector: td.t
    title|append:
      selector: td.e
      filters:
        - name: prepend
          args: " "
    _full:
      text: "{{ .Result.title }}!"
    description:
      text: "{{ .Result._full }}"
    category:
      selector: td.c
    categorydesc|append:
      selector: td.h
    size:
      text: 1
    seeders:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`)
	rels, err := search(t, srv, def, "x")
	require.NoError(t, err)
	require.Len(t, rels, 1)
	assert.Equal(t, "Show S01E01", rels[0].Title)
	assert.Equal(t, "Show S01E01!", rels[0].Description, ".Result.title is the appended value")
	assert.Equal(t, []newznab.CategoryID{newznab.CatTV, newznab.CatMoviesHD}, rels[0].Categories,
		"categorydesc|append unions onto category's mapping")

	def = loadFeature(t, `search:
  path: search
  rows:
    selector: tr
  fields:
    title:
      selector: td.t
    category:
      selector: td.c
    categorydesc|noappend:
      selector: td.h
    size:
      text: 1
    seeders:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`)
	rels, err = search(t, srv, def, "x")
	require.NoError(t, err)
	require.Len(t, rels, 1)
	assert.Equal(t, []newznab.CategoryID{newznab.CatMoviesHD}, rels[0].Categories, "|noappend replaces")
}

func TestSearchInheritInputsFalseDropsSearchInputs(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		_, _ = io.WriteString(w, `<table></table>`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  paths:
    - path: a
    - path: b
      inheritinputs: false
      inputs:
        own: "1"
  inputs:
    shared: "s"
  rows:
    selector: tr
`+htmlRowFields)
	_, err := search(t, srv, def, "x")
	require.NoError(t, err)
	assert.Equal(t, []string{"shared=s", "own=1"}, queries)
}

// TestHTMLCaseKeysAreSelectorsTriedInFileOrder: on an HTML page a case key
// is a CSS selector, the first that matches the selection (or a descendant)
// wins, and "*" is the universal selector written last. Until X8a the keys
// were compared to the row's text and the map was a Go map, so every HTML
// freeleech marker fell through to "*".
func TestHTMLCaseKeysAreSelectorsTriedInFileOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<table>
<tr><td class="t">Free</td><td><img class="free"><img class="half"></td></tr>
<tr><td class="t">Half</td><td><img class="half"></td></tr>
<tr><td class="t">Full</td><td></td></tr>
</table>`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  path: search
  rows:
    selector: tr
  fields:
    title:
      selector: td.t
    downloadvolumefactor:
      case:
        img.free: 0
        img.half: 0.5
        "*": 1
    size:
      text: 1
    seeders:
      text: 1
    category:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`)
	rels, err := search(t, srv, def, "x")
	require.NoError(t, err)
	require.Equal(t, []string{"Free", "Half", "Full"}, titles(rels))
	factors := make([]float64, 0, len(rels))
	for _, r := range rels {
		require.NotNil(t, r.DownloadVolumeFactor)
		factors = append(factors, *r.DownloadVolumeFactor)
	}
	assert.Equal(t, []float64{0, 0.5, 1}, factors, "first matching selector wins, in file order; * falls back")
}

// TestLoadAcceptsALeadingByteOrderMark: YAML allows a BOM at the start of a
// stream and torrent-pirat.yml in the v11 corpus has one; goccy/go-yaml
// read it as part of the first key and every such definition failed Load.
func TestLoadAcceptsALeadingByteOrderMark(t *testing.T) {
	body := fmt.Sprintf(defHeader, "UTF-8") + `search:
  path: search
  rows:
    selector: tr
` + htmlRowFields
	def, err := cardigann.Load(append([]byte{0xEF, 0xBB, 0xBF}, body...))
	require.NoError(t, err)
	assert.Equal(t, "feature", def.ID)
}

// TestLoginCookieReadsTheCookieSetting: every cookie-method definition in
// the v11 corpus declares a `cookie` setting holding a pasted Cookie header
// and no login.cookies; the method read only login.cookies, so each logged
// in with no cookies.
func TestLoginCookieReadsTheCookieSetting(t *testing.T) {
	def := loadFeature(t, `settings:
  - {name: cookie, type: text, label: Cookie}
login:
  method: cookie
search:
  path: search
  rows:
    selector: tr
`+htmlRowFields)
	cfg, err := cardigann.NewConfig(def, "https://tracker.example/", map[string]string{"cookie": " uid=1; pass=abc=; "})
	require.NoError(t, err)
	sess, err := cardigann.Engine{}.Login(context.Background(), def, cfg)
	require.NoError(t, err)
	assert.Equal(t, "uid=1; pass=abc=", sess.CookieHeader())

	cfg, err = cardigann.NewConfig(def, "https://tracker.example/", map[string]string{})
	require.NoError(t, err)
	_, err = cardigann.Engine{}.Login(context.Background(), def, cfg)
	require.Error(t, err, "a cookie login with no cookie can only send unauthenticated requests")
}

// TestLoginFormSendsLoginCookies: login.cookies (header form) go with a
// form login's requests, as Prowlarr sends them.
func TestLoginFormSendsLoginCookies(t *testing.T) {
	var landing, submit string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		landing = r.Header.Get("Cookie")
		_, _ = io.WriteString(w, loginFormPage)
	})
	mux.HandleFunc("POST /do-login", func(w http.ResponseWriter, r *http.Request) {
		submit = r.Header.Get("Cookie")
		_, _ = io.WriteString(w, "<html>ok</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	def := loadFeature(t, strings.Replace(loginFormDefinition, "  form: \"#login\"\n", "  form: \"#login\"\n  cookies: [\"JAVA=OK\"]\n", 1))
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"username": "u", "password": "p"})
	require.NoError(t, err)
	_, err = cardigann.Engine{HTTP: srv.Client()}.Login(context.Background(), def, cfg)
	require.NoError(t, err)
	assert.Equal(t, "JAVA=OK", landing)
	assert.Equal(t, "JAVA=OK", submit)
}

// TestDownloadTemplatesSeeDownloadUri: 13 corpus definitions build their
// download link from .DownloadUri (the details link being resolved) --
// .DownloadUri.Query.id above all. The field was never set, and was spelt
// DownloadURI, so each of those downloads failed to render.
func TestDownloadTemplatesSeeDownloadUri(t *testing.T) {
	var fetched string
	mux := http.NewServeMux()
	mux.HandleFunc("/details.php", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<a class="dl" href="/dl.php?id=OTHER">x</a><a class="dl" href="/dl.php?id=42">x</a>`)
	})
	mux.HandleFunc("/dl.php", func(w http.ResponseWriter, r *http.Request) {
		fetched = r.URL.Query().Get("id")
		_, _ = io.WriteString(w, fakeTorrent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	def := loadFeature(t, `download:
  selectors:
    - selector: "a.dl[href$=\"id={{ .DownloadUri.Query.id }}\"]"
      attribute: href
search:
  path: search
  rows:
    selector: tr
`+htmlRowFields)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{})
	require.NoError(t, err)
	rc, err := cardigann.Engine{HTTP: srv.Client()}.Download(context.Background(), def, cfg, "details.php?id=42")
	require.NoError(t, err)
	_ = rc.Close()
	assert.Equal(t, "42", fetched)
}

// TestXMLResponsesAreQueriedWithCSS: every XML definition in the v11 corpus
// writes CSS selectors ("rss > channel > item", "[name=seeders]"), which
// Prowlarr runs over an XML DOM. This package used XPath, and xmlquery's
// Find panics on a CSS selector -- each of those definitions would have
// crashed the search.
func TestXMLResponsesAreQueriedWithCSS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="windows-1251"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel><title>feed</title>
<item>
  <title>Some.Movie.2024 &amp; Friends&nbsp;1080p</title>
  <link>https://tracker.example/details/1</link>
  <pubDate>Thu, 17 Sep 2026 10:00:00 +0000</pubDate>
  <enclosure url="https://tracker.example/dl/1.torrent" length="4508876800" type="application/x-bittorrent"/>
  <torznab:attr name="seeders" value="12"/>
  <torznab:attr name="category" value="1"/>
</item>
</channel></rss>`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  paths:
    - path: rss
      response:
        type: xml
  rows:
    selector: rss > channel > item
  fields:
    title:
      selector: title
    details:
      selector: link
    download:
      selector: enclosure
      attribute: url
    size:
      selector: enclosure
      attribute: length
    seeders:
      selector: "[name=seeders]"
      attribute: value
    category:
      selector: "[name=category]"
      attribute: value
    date:
      selector: pubDate
`)
	var rels []torznab.Release
	var err error
	require.NotPanics(t, func() { rels, err = search(t, srv, def, "x") })
	require.NoError(t, err)
	require.Len(t, rels, 1)
	r := rels[0]
	assert.Equal(t, "Some.Movie.2024 & Friends 1080p", r.Title, "XML and HTML entities decode")
	assert.Equal(t, "https://tracker.example/details/1", r.CommentURL, "<link> keeps its text (not HTML's void element)")
	assert.Equal(t, "https://tracker.example/dl/1.torrent", r.Link)
	assert.EqualValues(t, 4508876800, r.Size)
	require.NotNil(t, r.Seeders)
	assert.EqualValues(t, 12, *r.Seeders)
	assert.Equal(t, []newznab.CategoryID{newznab.CatMovies}, r.Categories)
	assert.Equal(t, time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC), r.PubDate.UTC(), "pubDate matches camelCase")
}

// TestJSONSelectorFilters: JSON definitions use three CSS-like filters on
// rows and fields (Prowlarr's JsonParseFieldSelector). Without them a rows
// selector such as "data.data:not(blocked)" was a gjson path that matched
// nothing, and the search silently found zero results.
func TestJSONSelectorFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"data":[
			{"name":"Keep","size":1,"ref":"tt0000001","poster":"https://img/1.jpg"},
			{"name":"Blocked","size":2,"blocked":true},
			{"name":"NoRef","size":3,"ref":"none","poster":"ftp://img/3.jpg"}]}}`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  paths:
    - path: api
      response:
        type: json
  rows:
    selector: data.data:not(blocked)
  fields:
    title:
      selector: name
    size:
      selector: size
    seeders:
      text: 1
    category:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
    imdbid:
      selector: ref:contains(tt)
      optional: true
    poster:
      selector: poster:has(:contains(https))
      optional: true
`)
	rels, err := search(t, srv, def, "x")
	require.NoError(t, err)
	require.Equal(t, []string{"Keep", "NoRef"}, titles(rels), ":not drops the blocked row")
	assert.Equal(t, "tt0000001", rels[0].IDs["imdb"], ":contains keeps a matching value")
	assert.Empty(t, rels[1].IDs["imdb"], ":contains drops a value without the text")
	assert.Equal(t, "https://img/1.jpg", rels[0].Poster, "a nested :has(:contains()) holds")
	assert.Empty(t, rels[1].Poster)

	def = loadFeature(t, `search:
  paths:
    - path: api
      response:
        type: json
  rows:
    selector: $
  fields:
    title:
      selector: data.data.0.name
    size:
      text: 1
    seeders:
      text: 1
    category:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"data":{"data":[{"name":"Root"}]}}]`)
	}))
	defer srv2.Close()
	rels, err = search(t, srv2, def, "x")
	require.NoError(t, err)
	assert.Equal(t, []string{"Root"}, titles(rels), "$ is the document root")
}

// TestSearchPathSubstitutionsAreURLEncoded pins Prowlarr's and Jackett's
// search path rendering (ApplyGoTemplateText with WebUtility.UrlEncode, then
// "+" as "%20"): a keyword's "/", "?", "&" and "#" are escaped where they
// are substituted, so they cannot add a path segment or start a query,
// while the path's own "/" and "?" stay. A path that carries its whole
// query keeps it -- before gap fix Z6 an empty input set replaced it with
// nothing.
func TestSearchPathSubstitutionsAreURLEncoded(t *testing.T) {
	type got struct{ path, rawPath, rawQuery string }
	var reqs []got
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, got{r.URL.Path, r.URL.EscapedPath(), r.URL.RawQuery})
		_, _ = io.WriteString(w, `<table></table>`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  paths:
    - path: "search/{{ .Keywords }}/1/"
    - path: "browse.php?q={{ .Keywords }}&page=0"
  rows:
    selector: tr
`+htmlRowFields)
	_, err := search(t, srv, def, "AC/DC? Live & Loud #1")
	require.NoError(t, err)
	require.Len(t, reqs, 2)
	assert.Equal(t, "/search/AC%2FDC%3F%20Live%20%26%20Loud%20%231/1/", reqs[0].rawPath)
	assert.Equal(t, "/search/AC/DC? Live & Loud #1/1/", reqs[0].path, "one segment, decoded")
	assert.Equal(t, "/browse.php", reqs[1].path)
	assert.Equal(t, "q=AC%2FDC%3F%20Live%20%26%20Loud%20%231&page=0", reqs[1].rawQuery)
}

// TestSearchRawInputIsSplitIntoEncodedPairs pins "$raw" as Prowlarr's
// GetRequest and Jackett's PerformQuery build it: rendered with its
// substitutions URL-encoded, split on "&" into key=value pairs, and each
// value encoded again beside the other inputs (so a raw keyword's space
// reaches the tracker as Prowlarr sends it, "+" escaped). It used to be
// appended verbatim, sending a keyword's space and "&" unencoded. A path's
// own query is kept ahead of the inputs.
func TestSearchRawInputIsSplitIntoEncodedPairs(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `<table></table>`)
	}))
	defer srv.Close()

	def := loadFeature(t, `search:
  paths:
    - path: "index.php?do=search"
  inputs:
    $raw: "name={{ .Keywords }}&&{{ range .Categories }}c{{.}}=1&{{end}}flag"
    type: all
  rows:
    selector: tr
`+htmlRowFields)
	_, err := search(t, srv, def, "a&b c")
	require.NoError(t, err)
	values, err := url.ParseQuery(rawQuery)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rawQuery, "do=search&"), rawQuery)
	assert.Equal(t, []string{"a%26b+c"}, values["name"], "the keyword's own & does not split the pair")
	assert.Equal(t, []string{""}, values["flag"], "a key with no = is a key with an empty value")
	assert.Equal(t, []string{"all"}, values["type"])
}

// TestLoadAcceptsATabInsideADoubleQuotedFlowScalar: a literal TAB inside a
// double-quoted scalar is valid YAML (it is the same character as "\t"), but
// goccy/go-yaml v1.19.2 loses its place after one inside a flow mapping and
// refuses a later, valid line. The bundled uztracker.yml was refused for
// exactly this ("[498:66] found an invalid key for this map", from a TAB in
// a category desc on line 200) until gap fix Z6.
func TestLoadAcceptsATabInsideADoubleQuotedFlowScalar(t *testing.T) {
	header := strings.Replace(fmt.Sprintf(defHeader, "UTF-8"),
		`{id: 3, cat: Movies/HD, desc: "HD"}`, "{id: 3, cat: Movies/HD, desc: \" |- HD\tHi-Res\"}", 1)
	def, err := cardigann.Load([]byte(header + `search:
  path: search
  rows:
    selector: tr[id^="tor_"]:has(a[href^="/dl/"]), tr[id^="tor_"]:has(a[href^="magnet:?xt="])
` + htmlRowFields))
	require.NoError(t, err)
	assert.Equal(t, `tr[id^="tor_"]:has(a[href^="/dl/"]), tr[id^="tor_"]:has(a[href^="magnet:?xt="])`, def.Search.Rows.Selector)
	var desc string
	for _, m := range def.Caps.CategoryMappings {
		if m.ID == "3" {
			desc = m.Desc
		}
	}
	assert.Equal(t, " |- HD\tHi-Res", desc, "the TAB is kept as the character it is")
}
