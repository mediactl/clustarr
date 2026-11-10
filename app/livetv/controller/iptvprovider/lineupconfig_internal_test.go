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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
)

// fixtureToken is no credential: the fake Secret's value, which the tests
// follow into the tuner's config.
const fixtureToken = "fixture-token"

func secretReader(t *testing.T, secrets ...*corev1.Secret) *fake.ClientBuilder {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	b := fake.NewClientBuilder().WithScheme(s)
	for _, sec := range secrets {
		b = b.WithObjects(sec)
	}
	return b
}

func guideSourcesProvider(secret, key string) *clustarrv1.IPTVProvider {
	p := m3uProvider("https://lists.example/all.m3u", "")
	p.Spec.GuideSources = &clustarrv1.GuideSources{Plex: &clustarrv1.PlexGuideSource{
		Lineups: []string{plexLineupID, "657cb9b8a698374a7a2dbca0"},
	}}
	p.Spec.GuideSources.Plex.CredentialsRef.Name = secret
	p.Spec.GuideSources.Plex.CredentialsRef.Key = key
	return p
}

// TestTheGuideSourcesTokenIsReadLikeThePlexGuides: spec.guideSources.plex's
// token is read from its Secret key, trimmed; a missing Secret or key is the
// guide source's failure (GuideSourcesReady), never Valid=False, and the
// tuner is told why it fetches nothing.
func TestTheGuideSourcesTokenIsReadLikeThePlexGuides(t *testing.T) {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "plex-token"}, Data: map[string][]byte{"token": []byte(fixtureToken + "\n")}}
	r := fakeReconciler(t, testAddress)
	r.SecretReader = secretReader(t, sec).Build()

	res, valid := r.resolve(context.Background(), guideSourcesProvider("plex-token", "token"))
	require.Equal(t, metav1.ConditionTrue, valid.Status, valid.Message)
	assert.Empty(t, res.guideTokenReason)
	c := buildConfig(guideSourcesProvider("plex-token", "token"), nil, res, nil)
	require.NotNil(t, c.GuideSources)
	assert.Equal(t, fixtureToken, c.GuideSources.Token, "trimmed of the newline")
	assert.Empty(t, c.GuideSources.TokenError)
	assert.Equal(t, []string{plexLineupID, "657cb9b8a698374a7a2dbca0"}, c.GuideSources.Lineups)
	assert.Equal(t, clustarrv1.DefaultGridRefresh, c.GuideSources.Refresh)
	assert.Equal(t, int(clustarrv1.DefaultGridRate), c.GuideSources.Rate)

	for name, tc := range map[string]struct {
		secret, key, reason string
	}{
		"no Secret": {"absent", "token", clustarrv1.ReasonSecretNotFound},
		"no key":    {"plex-token", "other", clustarrv1.ReasonSecretKeyNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			p := guideSourcesProvider(tc.secret, tc.key)
			res, valid := r.resolve(context.Background(), p)
			assert.Equal(t, metav1.ConditionTrue, valid.Status, "another system's Secret is no reason for Valid=False")
			assert.Equal(t, tc.reason, res.guideTokenReason)
			assert.Contains(t, res.guideTokenError, tc.secret)
			assert.Empty(t, res.guideToken)
			c := buildConfig(p, nil, res, nil)
			require.NotNil(t, c.GuideSources)
			assert.Empty(t, c.GuideSources.Token)
			assert.Equal(t, res.guideTokenError, c.GuideSources.TokenError)
			assert.NotContains(t, res.guideTokenError, fixtureToken)
		})
	}
}

// TestBuildConfigCarriesTheLineup: under an enabled lineup a channel's name
// and group fall back to what the planner decided, and the channel and its
// block carry the category; a dry run moves nothing the tuner shows. Trim
// and the guide sources' settings are passed as they are.
func TestBuildConfigCarriesTheLineup(t *testing.T) {
	named := catChannel("1001", "News")
	named.Status.Lineup.Name = "CNN"
	own := catChannel("1002", "News")
	own.Spec.Name, own.Spec.Group = "CNN International", "World"
	own.Status.Lineup.Name = "CNN INTERNATIONAL"
	none := catChannel("1500", "")
	chans := []clustarrv1.IPTVChannel{named, own, none}

	p := lineupProvider(true)
	p.Spec.Lineup.Trim = []string{`^USA:\s*`, `\s*\[1080p\]`}
	rate := int32(5)
	p.Spec.GuideSources = &clustarrv1.GuideSources{Plex: &clustarrv1.PlexGuideSource{Refresh: &metav1.Duration{Duration: 6 * time.Hour}, Rate: &rate}}
	pl := plan{blocks: []clustarrv1.BlockStatus{{Start: "1001", Category: "News"}}, numbers: map[string][]string{"1001": {"1001", "1002"}}}
	c := buildConfig(p, tunerBlocks(pl), resolved{guideToken: fixtureToken}, chans)
	assert.Equal(t, []string{`^USA:\s*`, `\s*\[1080p\]`}, c.Trim)
	require.NotNil(t, c.GuideSources)
	assert.Equal(t, 6*time.Hour, c.GuideSources.Refresh)
	assert.Equal(t, 5, c.GuideSources.Rate)
	assert.Empty(t, c.GuideSources.Lineups)
	require.Len(t, c.Blocks, 1)
	assert.Equal(t, "News", c.Blocks[0].Category)

	require.Len(t, c.Channels, 3)
	assert.Equal(t, "CNN", c.Channels[0].Name, "status.lineup.name when spec.name is unset")
	assert.Equal(t, "News", c.Channels[0].Group, "the category when spec.group is unset")
	assert.Equal(t, "News", c.Channels[0].Category)
	assert.Equal(t, "CNN International", c.Channels[1].Name, "a person's name wins")
	assert.Equal(t, "World", c.Channels[1].Group, "a person's group wins")
	assert.Equal(t, "News", c.Channels[1].Category)
	assert.Empty(t, c.Channels[2].Name)
	assert.Empty(t, c.Channels[2].Group)
	assert.Empty(t, c.Channels[2].Category)

	// A dry run is shown in the channels' status, never in what Plex shows.
	p.Spec.Lineup.Enabled = false
	c = buildConfig(p, nil, resolved{}, chans)
	assert.Equal(t, []string{`^USA:\s*`, `\s*\[1080p\]`}, c.Trim)
	assert.Empty(t, c.Channels[0].Name)
	assert.Empty(t, c.Channels[0].Group)
	assert.Empty(t, c.Channels[0].Category)
	assert.Equal(t, "CNN International", c.Channels[1].Name)

	// No spec.lineup, no spec.guideSources: neither reaches the tuner.
	c = buildConfig(&clustarrv1.IPTVProvider{}, nil, resolved{}, chans)
	assert.Nil(t, c.Trim)
	assert.Nil(t, c.GuideSources)
	assert.Empty(t, c.Channels[0].Category)
}
