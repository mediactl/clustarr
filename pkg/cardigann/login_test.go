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
