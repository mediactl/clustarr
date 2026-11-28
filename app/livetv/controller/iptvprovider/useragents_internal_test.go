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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
)

func TestTheUserAgentPoolReachesTheTunersConfigAsACopy(t *testing.T) {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon"}}
	p.Spec.UserAgent = "clustarr"
	p.Spec.Stream.UserAgents = []string{"VLC/3.0.20 LibVLC/3.0.20", "VLC/3.0.21 LibVLC/3.0.21", "VLC/3.0.18 LibVLC/3.0.18"}

	c := buildConfig(p, nil, resolved{}, nil)

	assert.Equal(t, p.Spec.Stream.UserAgents, c.UserAgents)
	assert.Equal(t, "clustarr", c.UserAgent, "playlist and guide requests keep spec.userAgent")
	c.UserAgents[0] = "changed"
	assert.Equal(t, "VLC/3.0.20 LibVLC/3.0.20", p.Spec.Stream.UserAgents[0], "the tuner's config is its own")
}
