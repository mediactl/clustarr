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

package extmetrics

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The ConfigMap the kube-apiserver publishes its front-proxy trust in.
const (
	AuthConfigMapNamespace = "kube-system"
	AuthConfigMapName      = "extension-apiserver-authentication"
)

const (
	keyClientCA        = "requestheader-client-ca-file"
	keyAllowedNames    = "requestheader-allowed-names"
	keyUsernameHeaders = "requestheader-username-headers"
	keyGroupHeaders    = "requestheader-group-headers"
	keyExtraPrefixes   = "requestheader-extra-headers-prefix"
)

// AuthConfig is the front-proxy trust: the CA that signs the
// kube-apiserver's proxy client certificate, the CNs it may carry, and the
// headers it names the original user in.
type AuthConfig struct {
	ClientCAs           *x509.CertPool
	AllowedNames        []string
	UsernameHeaders     []string
	GroupHeaders        []string
	ExtraHeaderPrefixes []string
}

// ParseAuthConfig reads the ConfigMap's data.
func ParseAuthConfig(data map[string]string) (AuthConfig, error) {
	var cfg AuthConfig
	ca := data[keyClientCA]
	if ca == "" {
		return cfg, fmt.Errorf("extmetrics: %s has no %s; the kube-apiserver runs without --requestheader-client-ca-file", AuthConfigMapName, keyClientCA)
	}
	cfg.ClientCAs = x509.NewCertPool()
	if !cfg.ClientCAs.AppendCertsFromPEM([]byte(ca)) {
		return cfg, fmt.Errorf("extmetrics: %s holds no PEM certificate", keyClientCA)
	}
	for key, dst := range map[string]*[]string{
		keyAllowedNames: &cfg.AllowedNames, keyUsernameHeaders: &cfg.UsernameHeaders,
		keyGroupHeaders: &cfg.GroupHeaders, keyExtraPrefixes: &cfg.ExtraHeaderPrefixes,
	} {
		if v := data[key]; v != "" {
			if err := json.Unmarshal([]byte(v), dst); err != nil {
				return cfg, fmt.Errorf("extmetrics: %s is not a JSON array: %w", key, err)
			}
		}
	}
	if len(cfg.UsernameHeaders) == 0 {
		return cfg, errors.New("extmetrics: " + keyUsernameHeaders + " names no header")
	}
	return cfg, nil
}

// Authenticate returns the front-proxied user, or the status refusing the
// request: 401 without a verified client certificate or a user header,
// 403 for a certificate whose CN is not allowed (an empty list allows any
// CN the CA signed). No SubjectAccessReview: the kube-apiserver has already
// authenticated and authorized the caller before proxying (§9.4).
func (a AuthConfig) Authenticate(r *http.Request) (user string, status int) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		return "", http.StatusUnauthorized
	}
	cn := r.TLS.VerifiedChains[0][0].Subject.CommonName
	if len(a.AllowedNames) > 0 && !slices.Contains(a.AllowedNames, cn) {
		return "", http.StatusForbidden
	}
	for _, h := range a.UsernameHeaders {
		if u := r.Header.Get(h); u != "" {
			return u, 0
		}
	}
	return "", http.StatusUnauthorized
}

type userKey struct{}

// UserFrom is the authenticated front-proxied user of a request.
func UserFrom(ctx context.Context) string {
	u, _ := ctx.Value(userKey{}).(string)
	return u
}

// FrontProxyAuth authenticates every request against current() before
// next sees it. A nil config (material not loaded) is 503.
func FrontProxyAuth(current func() *AuthConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := current()
		if cfg == nil {
			writeStatus(w, http.StatusServiceUnavailable, metav1.StatusReasonServiceUnavailable, "the front-proxy CA is not loaded yet")
			return
		}
		user, status := cfg.Authenticate(r)
		switch status {
		case 0:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
		case http.StatusForbidden:
			writeStatus(w, status, metav1.StatusReasonForbidden, "the client certificate's CN is not an allowed front proxy")
		default:
			writeStatus(w, status, metav1.StatusReasonUnauthorized, "a front-proxy client certificate and user header are required")
		}
	})
}
