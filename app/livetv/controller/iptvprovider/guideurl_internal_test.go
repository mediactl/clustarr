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

package iptvprovider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
)

func TestValidateSourceURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://iptv-epg.org/files/0123/epg-us.xml":                                  true,
		"http://panel.example:8080/xmltv.php?username={username}&password={password}": true,
		" https://example.com/guide.xml ":                                             true,
		"ftp://example.com/guide.xml":                                                 false,
		"https:///guide.xml":                                                          false,
		"https://user:pass@example.com/guide.xml":                                     false,
		"example.com/guide.xml":                                                       false,
	} {
		err := ValidateSourceURL(raw)
		assert.Equal(t, ok, err == nil, "%q: %v", raw, err)
	}
}

func m3uProvider(playlistURL, secret string) *clustarrv1.IPTVProvider {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "lists"}}
	p.Spec.Type = clustarrv1.ProviderTypeM3U
	p.Spec.PlaylistURL = playlistURL
	p.Spec.CredentialsRef.Name = secret
	return p
}

func TestBuildConfigForAnM3UProvider(t *testing.T) {
	c := buildConfig(m3uProvider("https://lists.example/{username}.m3u", ""), nil, resolved{username: "u", password: "p"}, nil)
	assert.Equal(t, "M3U", c.Type)
	assert.Equal(t, "https://lists.example/{username}.m3u", c.PlaylistURLTemplate)
	assert.Empty(t, c.Server)
	x := buildConfig(&clustarrv1.IPTVProvider{}, nil, resolved{server: "https://panel.example"}, nil)
	assert.Equal(t, "Xtream", x.Type, "an unset type is Xtream")
}

func TestAnM3UProviderNeedsNoSecret(t *testing.T) {
	r := fakeReconciler(t, testAddress)
	r.SecretReader = r.Client
	res, valid := r.resolve(context.Background(), m3uProvider("https://lists.example/all.m3u", ""))
	assert.Equal(t, metav1.ConditionTrue, valid.Status, valid.Message)
	assert.Empty(t, res.username)

	_, valid = r.resolve(context.Background(), m3uProvider("lists.example/all.m3u", ""))
	assert.Equal(t, metav1.ConditionFalse, valid.Status)
	assert.Equal(t, clustarrv1.ReasonInvalidPlaylistURL, valid.Reason)

	_, valid = r.resolve(context.Background(), m3uProvider("https://lists.example/all.m3u", "missing"))
	assert.Equal(t, clustarrv1.ReasonSecretNotFound, valid.Reason, "a named Secret must exist")
}

func TestAnM3UProviderWithoutAGuideURLNeedsNoGuide(t *testing.T) {
	_, guide := fetchConditions(m3uProvider("https://lists.example/all.m3u", ""), tuner.Snapshot{PlaylistFetched: true}, true)
	assert.Equal(t, metav1.ConditionTrue, guide.Status)
	assert.Equal(t, "NoGuide", guide.Reason)
}
