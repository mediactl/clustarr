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

package importliststate

import (
	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/trakt"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The owned Trakt token Secret's keys. Design spec §8.7 calls this "the
// token pair rotated atomically in the owned Secret" -- atomically meaning
// one apply of the whole data map, which the ImportList controller's
// SecretTokenStore (app/import/controller/importlist/tokens.go), the
// Secret's one writer (ADR-0019 §7.6, W30), does.
const (
	TraktSecretKeyAccessToken         = "accessToken"
	TraktSecretKeyRefreshToken        = "refreshToken"
	TraktSecretKeyExpiresAt           = "expiresAt"
	TraktSecretKeyDeviceCode          = "deviceCode"
	TraktSecretKeyDeviceCodeExpiresAt = "deviceCodeExpiresAt"
	TraktSecretKeyPollIntervalSeconds = "pollIntervalSeconds"
	// TraktSecretKeyIssuedAt is when the controller stored the current token
	// pair: with expiresAt it gives the token's lifetime, which bounds how
	// early the controller refreshes it (a token that lives less than two
	// refresh windows is refreshed at half its life, never on every pass).
	TraktSecretKeyIssuedAt = "issuedAt"
)

// TraktTokenSecretName returns the deterministic name of the Secret one
// ImportList's Trakt device flow uses, both for the rotating access/refresh
// token pair and for the in-flight device code between Start and the poll
// that authorizes it. It is owned by, and garbage-collected with, the
// ImportList, so nothing else needs to clean it up.
func TraktTokenSecretName(listName string) string {
	return k8s.ChildName(listName, "trakt-token")
}

// TokenReader reads one ImportList's Trakt token Secret and never writes
// it. The import agent's list worker reads the access token through it (a
// token Trakt refuses becomes the snapshot's NeedsToken, never a refresh
// there: Trakt rotates refresh tokens, so one refresher removes the race two
// writers had); the ImportList controller's SecretTokenStore embeds it.
type TokenReader struct {
	// Client reads the Secret. Secrets are never cached (spec 2026-10-06
	// §5.6), so this is a live get.
	Client client.Reader
	// Namespace and ListName name the ImportList the Secret belongs to.
	Namespace, ListName string
}

// NewTokenReader returns a reader of list's Trakt token Secret through c.
func NewTokenReader(c client.Reader, list *catalogv1alpha1.ImportList) *TokenReader {
	return &TokenReader{Client: c, Namespace: list.Namespace, ListName: list.Name}
}

// SecretKey names the Secret.
func (r *TokenReader) SecretKey() types.NamespacedName {
	return types.NamespacedName{Namespace: r.Namespace, Name: TraktTokenSecretName(r.ListName)}
}

// Data reads the Secret's data and resourceVersion, treating "not found" as
// an empty map and an empty resourceVersion rather than an error: the
// Secret does not exist until the controller first stores a token or a
// device code.
func (r *TokenReader) Data(ctx context.Context) (map[string][]byte, string, error) {
	var sec corev1.Secret
	if err := r.Client.Get(ctx, r.SecretKey(), &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return map[string][]byte{}, "", nil
		}
		return nil, "", fmt.Errorf("importlist: read trakt token secret %s: %w", r.SecretKey(), err)
	}
	if sec.Data == nil {
		return map[string][]byte{}, sec.ResourceVersion, nil
	}
	return sec.Data, sec.ResourceVersion, nil
}

// Load returns the stored token pair; ok is false when none is stored.
func (r *TokenReader) Load(ctx context.Context) (importlist.Token, bool, error) {
	data, _, err := r.Data(ctx)
	if err != nil {
		return importlist.Token{}, false, err
	}
	tok, ok := TokenFrom(data)
	return tok, ok, nil
}

// LoadDeviceCode returns the in-flight device code, if one is stored. Only
// DeviceCode, ExpiresAt and Interval are populated -- UserCode and
// VerificationURL are not secret and live on ImportList.status.auth
// instead, which is where the controller reads them back from when it
// needs to know what it last showed the user.
func (r *TokenReader) LoadDeviceCode(ctx context.Context) (trakt.DeviceCode, bool, error) {
	data, _, err := r.Data(ctx)
	if err != nil {
		return trakt.DeviceCode{}, false, err
	}
	dc, ok := DeviceCodeFrom(data)
	return dc, ok, nil
}

// TokenFrom decodes the token pair from the Secret's data; ok is false when
// no access token is stored.
func TokenFrom(data map[string][]byte) (importlist.Token, bool) {
	access := string(data[TraktSecretKeyAccessToken])
	if access == "" {
		return importlist.Token{}, false
	}
	expiresAt, _ := time.Parse(time.RFC3339, string(data[TraktSecretKeyExpiresAt]))
	return importlist.Token{
		AccessToken:  access,
		RefreshToken: string(data[TraktSecretKeyRefreshToken]),
		ExpiresAt:    expiresAt,
	}, true
}

// IssuedAtFrom decodes when the stored token pair was stored, or the zero
// time when the Secret does not say.
func IssuedAtFrom(data map[string][]byte) time.Time {
	at, _ := time.Parse(time.RFC3339, string(data[TraktSecretKeyIssuedAt]))
	return at
}

// DeviceCodeFrom decodes the in-flight device code from the Secret's data;
// ok is false when none is stored.
func DeviceCodeFrom(data map[string][]byte) (trakt.DeviceCode, bool) {
	code := string(data[TraktSecretKeyDeviceCode])
	if code == "" {
		return trakt.DeviceCode{}, false
	}
	expiresAt, _ := time.Parse(time.RFC3339, string(data[TraktSecretKeyDeviceCodeExpiresAt]))
	intervalSeconds, _ := strconv.ParseInt(string(data[TraktSecretKeyPollIntervalSeconds]), 10, 64)
	return trakt.DeviceCode{
		DeviceCode: code,
		ExpiresAt:  expiresAt,
		Interval:   time.Duration(intervalSeconds) * time.Second,
	}, true
}
