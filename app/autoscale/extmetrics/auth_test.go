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

package extmetrics_test

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
)

func TestParseAuthConfig(t *testing.T) {
	proxy := newTestCA(t, time.Now())
	cm := authConfigMap(extmetrics.AuthConfigMapNamespace, extmetrics.AuthConfigMapName, proxy.certPEM, `["front-proxy-client"]`)
	cfg, err := extmetrics.ParseAuthConfig(cm.Data)
	require.NoError(t, err)
	require.NotNil(t, cfg.ClientCAs)
	assert.Equal(t, []string{"front-proxy-client"}, cfg.AllowedNames)
	assert.Equal(t, []string{"X-Remote-User"}, cfg.UsernameHeaders)
	assert.Equal(t, []string{"X-Remote-Group"}, cfg.GroupHeaders)
	assert.Equal(t, []string{"X-Remote-Extra-"}, cfg.ExtraHeaderPrefixes)

	for name, mutate := range map[string]func(map[string]string){
		"no front-proxy CA":         func(d map[string]string) { delete(d, "requestheader-client-ca-file") },
		"a CA that is not PEM":      func(d map[string]string) { d["requestheader-client-ca-file"] = "nope" },
		"allowed names not JSON":    func(d map[string]string) { d["requestheader-allowed-names"] = "front-proxy-client" },
		"no username header":        func(d map[string]string) { d["requestheader-username-headers"] = `[]` },
		"username headers not JSON": func(d map[string]string) { d["requestheader-username-headers"] = "X-Remote-User" },
	} {
		t.Run(name, func(t *testing.T) {
			data := authConfigMap(ns, "x", proxy.certPEM, `[]`).Data
			mutate(data)
			_, err := extmetrics.ParseAuthConfig(data)
			assert.Error(t, err)
		})
	}
}

func TestFrontProxyAuth(t *testing.T) {
	proxy := newTestCA(t, time.Now())
	allowed := proxy.clientCert(t, "front-proxy-client")
	intruder := proxy.clientCert(t, "intruder")
	restricted, err := extmetrics.ParseAuthConfig(authConfigMap(ns, "x", proxy.certPEM, `["front-proxy-client"]`).Data)
	require.NoError(t, err)
	open, err := extmetrics.ParseAuthConfig(authConfigMap(ns, "x", proxy.certPEM, `[]`).Data)
	require.NoError(t, err)

	const hpa = "system:serviceaccount:kube-system:horizontal-pod-autoscaler"
	chain := func(c tls.Certificate) *tls.ConnectionState {
		return &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{c.Leaf, proxy.cert}}}
	}
	for _, tc := range []struct {
		name   string
		cfg    *extmetrics.AuthConfig
		state  *tls.ConnectionState
		user   string
		status int
	}{
		{"no TLS at all", &restricted, nil, hpa, http.StatusUnauthorized},
		{"no verified client certificate", &restricted, &tls.ConnectionState{}, hpa, http.StatusUnauthorized},
		{"a CN the ConfigMap does not allow", &restricted, chain(intruder), hpa, http.StatusForbidden},
		{"an allowed CN with no user header", &restricted, chain(allowed), "", http.StatusUnauthorized},
		{"an allowed, identified front proxy", &restricted, chain(allowed), hpa, http.StatusOK},
		{"an empty allowed-names list allows any CN the CA signed", &open, chain(intruder), hpa, http.StatusOK},
		{"material not loaded yet", nil, chain(allowed), hpa, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = extmetrics.UserFrom(r.Context())
				_, _ = w.Write([]byte(`{"items":[{"value":"37"}]}`))
			})
			cfg := tc.cfg
			h := extmetrics.FrontProxyAuth(func() *extmetrics.AuthConfig { return cfg }, next)
			req := httptest.NewRequest(http.MethodGet, "/apis/external.metrics.k8s.io/v1beta1", nil)
			req.TLS = tc.state
			if tc.user != "" {
				req.Header.Set("X-Remote-User", tc.user)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code)
			if tc.status == http.StatusOK {
				assert.Equal(t, tc.user, seen)
				return
			}
			assert.Empty(t, seen, "a refused request never reaches the metrics handler")
			assert.Contains(t, rec.Body.String(), `"kind":"Status"`)
			assert.NotContains(t, rec.Body.String(), `"items"`, "no error response carries metric data")
			assert.NotContains(t, rec.Body.String(), `"37"`)
		})
	}
}
