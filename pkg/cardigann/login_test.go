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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

func TestEngineLoginForm(t *testing.T) {
	var gotUser, gotPass, gotCSRF string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/login":
			_, _ = w.Write(readTestdataBytes(t, "login-form.html"))
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			require.NoError(t, r.ParseForm())
			gotUser, gotPass, gotCSRF = r.Form.Get("username"), r.Form.Get("password"), r.Form.Get("csrf_token")
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/dashboard":
			// login.test: the logout link shows only to a live session.
			if c, err := r.Cookie("session"); err == nil && c.Value == "abc" {
				_, _ = w.Write([]byte(`<html><body><a class="logout" href="/logout">log out</a></body></html>`))
				return
			}
			_, _ = w.Write([]byte(`<html><body><a href="/login">log in</a></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	def, err := cardigann.Load(readTestdata(t, "login-form.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"username": "alice", "password": "s3cret"})
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client()}
	sess, err := eng.Login(context.Background(), def, cfg)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "alice", gotUser)
	assert.Equal(t, "s3cret", gotPass)
	assert.Equal(t, "tok-abc123", gotCSRF) // scraped via selectorinputs, not hand-typed
	require.Len(t, sess.Cookies, 1)
	assert.Equal(t, "session", sess.Cookies[0].Name)
}

func TestEngineLoginCookie(t *testing.T) {
	def, err := cardigann.Load(readTestdata(t, "login-cookie.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, "https://example.invalid/", map[string]string{"session_id": "xyz789"})
	require.NoError(t, err)

	eng := cardigann.Engine{}
	sess, err := eng.Login(context.Background(), def, cfg)
	require.NoError(t, err)
	require.Len(t, sess.Cookies, 1)
	assert.Equal(t, "session_id", sess.Cookies[0].Name)
	assert.Equal(t, "xyz789", sess.Cookies[0].Value)
}

func TestEngineLoginCookieMissingSettingErrors(t *testing.T) {
	def, err := cardigann.Load(readTestdata(t, "login-cookie.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, "https://example.invalid/", map[string]string{})
	require.NoError(t, err)

	eng := cardigann.Engine{}
	_, err = eng.Login(context.Background(), def, cfg)
	assert.Error(t, err)
}

func TestEngineLoginGetAppliesErrorSelectors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "good-key" {
			_, _ = w.Write([]byte(`<html><body><a href="/login">please log in</a></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body>ok</body></html>`))
	}))
	defer srv.Close()

	def := &cardigann.Definition{
		Links: []string{srv.URL + "/"},
		Login: &cardigann.LoginBlock{
			Method: "get", Path: "ping",
			Inputs: map[string]cardigann.Scalar{"apikey": "{{ .Config.apikey }}"},
			Error:  []cardigann.ErrorBlock{{Selector: `a[href="/login"]`}},
		},
	}
	eng := cardigann.Engine{HTTP: srv.Client()}

	badCfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"apikey": "wrong"})
	require.NoError(t, err)
	_, err = eng.Login(context.Background(), def, badCfg)
	assert.Error(t, err)

	goodCfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"apikey": "good-key"})
	require.NoError(t, err)
	_, err = eng.Login(context.Background(), def, goodCfg)
	assert.NoError(t, err)
}

func TestEngineLoginCaptchaRequiredSignal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><img class="captcha-image" src="/c.png"></body></html>`))
	}))
	defer srv.Close()

	def := &cardigann.Definition{
		Links: []string{srv.URL + "/"},
		Login: &cardigann.LoginBlock{
			Method: "form", Path: "login",
			Captcha: &cardigann.CaptchaBlock{Type: "image", Selector: "img.captcha-image", Input: "captcha"},
		},
	}
	eng := cardigann.Engine{HTTP: srv.Client()}
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
	require.NoError(t, err)

	_, err = eng.Login(context.Background(), def, cfg)
	var captchaErr *cardigann.CaptchaRequiredError
	require.ErrorAs(t, err, &captchaErr)
	assert.Equal(t, "image", captchaErr.Type)
	assert.Equal(t, "img.captcha-image", captchaErr.Selector)
	assert.Contains(t, err.Error(), `serves an image captcha ("img.captcha-image")`)
	assert.Contains(t, err.Error(), `set the Indexer Secret's "cookie" key`, "the condition names the workaround")
}

// TestEngineLoginCaptchaTakesAManualCookie is the documented workaround: a
// login page that serves its captcha takes the operator's browser cookie
// (the `cookie` setting) as the session instead of failing, and login.test
// still proves it -- a dead cookie fails the test.
func TestEngineLoginCaptchaTakesAManualCookie(t *testing.T) {
	var submitted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login" && r.Method == http.MethodPost:
			submitted = true
		case r.URL.Path == "/login":
			_, _ = w.Write([]byte(`<html><body><form><input name="u"></form><img class="captcha-image" src="/c.png"></body></html>`))
		case r.URL.Path == "/me":
			if c, err := r.Cookie("uid"); err == nil && c.Value == "7" {
				_, _ = w.Write([]byte(`<a class="logout">out</a>`))
				return
			}
			_, _ = w.Write([]byte(`<html><body>who?</body></html>`))
		}
	}))
	defer srv.Close()

	for _, method := range []string{"form", "post"} {
		def := &cardigann.Definition{
			Links: []string{srv.URL + "/"},
			Login: &cardigann.LoginBlock{
				Method: method, Path: "login",
				Captcha: &cardigann.CaptchaBlock{Type: "image", Selector: "img.captcha-image", Input: "captcha"},
				Test:    &cardigann.PageTestBlock{Path: "me", Selector: "a.logout"},
			},
		}
		eng := cardigann.Engine{HTTP: srv.Client()}

		cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"cookie": "uid=7; pass=abc"})
		require.NoError(t, err)
		sess, err := eng.Login(context.Background(), def, cfg)
		require.NoError(t, err, method)
		require.Len(t, sess.Cookies, 2, method)
		assert.Equal(t, "uid", sess.Cookies[0].Name)
		assert.False(t, submitted, "%s: the captcha form is never submitted", method)

		dead, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"cookie": "uid=8"})
		require.NoError(t, err)
		_, err = eng.Login(context.Background(), def, dead)
		var le *cardigann.LoginError
		require.ErrorAs(t, err, &le, "%s: login.test refuses an expired manual cookie", method)
	}
}

// TestEngineLoginPost covers the "post" login method, the one mode the
// brief's own quoted login_test.go content never exercises (form, cookie,
// get, and get's oneurl sibling all have coverage above/via the shared
// loginGet path) — added for this task's own completeness self-review
// ("every login mode").
func TestEngineLoginPost(t *testing.T) {
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, r.ParseForm())
		gotUser, gotPass = r.Form.Get("username"), r.Form.Get("password")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "xyz"})
	}))
	defer srv.Close()

	def := &cardigann.Definition{
		Links: []string{srv.URL + "/"},
		Login: &cardigann.LoginBlock{
			Method: "post", Path: "login",
			Inputs: map[string]cardigann.Scalar{
				"username": "{{ .Config.username }}",
				"password": "{{ .Config.password }}",
			},
		},
	}
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"username": "bob", "password": "hunter2"})
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client()}
	sess, err := eng.Login(context.Background(), def, cfg)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "bob", gotUser)
	assert.Equal(t, "hunter2", gotPass)
	require.Len(t, sess.Cookies, 1)
	assert.Equal(t, "session", sess.Cookies[0].Name)
}

// TestEngineLoginTestRunsForEveryMethod: login.test proves the session for
// a form, post, get and cookie login alike (Jackett's TestLogin after every
// DoLogin; Prowlarr's CheckIfLoginIsNeeded on every response), not only for
// a cookie login. A tracker that answers a bad login with a 200 and no
// login.error match is caught by the test page instead; a redirect or an
// HTTP error from the test page fails it too, and search.headers ride along.
func TestEngineLoginTestRunsForEveryMethod(t *testing.T) {
	var testHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Method == http.MethodGet && r.URL.Query().Get("apikey") == "" {
				_, _ = w.Write([]byte(`<html><body><form action="/login" method="post"><input name="username"></form></body></html>`))
				return
			}
			// Any credentials "work": the tracker sets its cookie whatever
			// was sent, and only the test page tells a real session apart.
			_ = r.ParseForm()
			if r.Form.Get("username") == "good" || r.URL.Query().Get("apikey") == "good" {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "live"})
			} else {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "dead"})
			}
		case "/me":
			testHeader = r.Header.Get("X-Test")
			if c, err := r.Cookie("session"); err == nil && c.Value == "live" {
				_, _ = w.Write([]byte(`<html><body><a class="logout">out</a></body></html>`))
				return
			}
			_, _ = w.Write([]byte(`<html><body>who are you</body></html>`))
		case "/gone":
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/broken":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	eng := cardigann.Engine{HTTP: srv.Client()}
	test := &cardigann.PageTestBlock{Path: "me", Selector: "a.logout"}
	search := cardigann.SearchBlock{Headers: map[string][]string{"X-Test": {"{{ .Config.username }}"}}}

	methods := map[string]*cardigann.LoginBlock{
		"form": {Method: "form", Path: "login", Inputs: map[string]cardigann.Scalar{"username": "{{ .Config.username }}"}},
		"post": {Method: "post", Path: "login", Inputs: map[string]cardigann.Scalar{"username": "{{ .Config.username }}"}},
		"get":  {Method: "get", Path: "login", Inputs: map[string]cardigann.Scalar{"apikey": "{{ .Config.username }}"}},
	}
	for name, lb := range methods {
		t.Run(name, func(t *testing.T) {
			lb.Test = test
			def := &cardigann.Definition{Links: []string{srv.URL + "/"}, Login: lb, Search: search}
			for _, user := range []string{"good", "bad"} {
				cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"username": user})
				require.NoError(t, err)
				sess, err := eng.Login(context.Background(), def, cfg)
				if user == "bad" {
					var le *cardigann.LoginError
					require.ErrorAs(t, err, &le)
					assert.Contains(t, le.Message, `login test selector "a.logout" did not match`)
					continue
				}
				require.NoError(t, err)
				require.NotNil(t, sess)
				assert.Equal(t, "good", testHeader, "search.headers ride on the test request")
			}
		})
	}

	cookieDef := func(path string) *cardigann.Definition {
		return &cardigann.Definition{
			Links:  []string{srv.URL + "/"},
			Login:  &cardigann.LoginBlock{Method: "cookie", Test: &cardigann.PageTestBlock{Path: path, Selector: "a.logout"}},
			Search: search,
		}
	}
	for path, want := range map[string]string{
		"me":     "",
		"gone":   "login test page redirected",
		"broken": "login test page returned HTTP 500",
		"json":   "", // the selector is checked on HTML only
	} {
		def := cookieDef(path)
		cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"cookie": "session=live"})
		require.NoError(t, err)
		_, err = eng.Login(context.Background(), def, cfg)
		if want == "" {
			assert.NoError(t, err, path)
			continue
		}
		var le *cardigann.LoginError
		require.ErrorAs(t, err, &le, path)
		assert.Equal(t, want, le.Message, path)
	}
}

func TestEngineDetectsCloudflareChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "cloudflare")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	def := &cardigann.Definition{Links: []string{srv.URL + "/"}}
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
	require.NoError(t, err)
	eng := cardigann.Engine{HTTP: srv.Client()}

	_, err = eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search"})
	var cfErr *cardigann.CloudflareChallengeError
	require.ErrorAs(t, err, &cfErr)
	assert.Equal(t, http.StatusForbidden, cfErr.StatusCode)
}
