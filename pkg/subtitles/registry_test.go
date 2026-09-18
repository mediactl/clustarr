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

package subtitles_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

type fakeProvider struct {
	name string
	caps subtitles.Capabilities
}

func (f fakeProvider) Name() string                         { return f.name }
func (f fakeProvider) Capabilities() subtitles.Capabilities { return f.caps }
func (f fakeProvider) HIVerifiable() bool                   { return f.caps.HashVerifiable }
func (f fakeProvider) Search(context.Context, subtitles.Query) ([]subtitles.Candidate, error) {
	return nil, nil
}
func (f fakeProvider) Download(context.Context, subtitles.Candidate) ([]byte, string, error) {
	return nil, "", nil
}

var _ subtitles.Provider = fakeProvider{} // compile-time interface conformance

func TestRegistryRegisterGetAll(t *testing.T) {
	r := subtitles.NewRegistry()
	os := fakeProvider{name: "opensubtitlescom", caps: subtitles.Capabilities{Movies: true, Episodes: true}}
	gd := fakeProvider{name: "gestdown", caps: subtitles.Capabilities{Episodes: true}}

	require.NoError(t, r.Register(os))
	require.NoError(t, r.Register(gd))

	got, ok := r.Get("gestdown")
	require.True(t, ok)
	assert.Equal(t, "gestdown", got.Name())

	_, ok = r.Get("missing")
	assert.False(t, ok)

	assert.Equal(t, []subtitles.Provider{os, gd}, r.All(), "All must preserve registration order")
}

func TestRegistryRegisterRejectsDuplicateNames(t *testing.T) {
	r := subtitles.NewRegistry()
	require.NoError(t, r.Register(fakeProvider{name: "gestdown"}))
	err := r.Register(fakeProvider{name: "gestdown"})
	assert.ErrorIs(t, err, subtitles.ErrDuplicateProvider)
}

func TestRegistryForFiltersByMediaKind(t *testing.T) {
	r := subtitles.NewRegistry()
	require.NoError(t, r.Register(fakeProvider{name: "opensubtitlescom", caps: subtitles.Capabilities{Movies: true, Episodes: true}}))
	require.NoError(t, r.Register(fakeProvider{name: "gestdown", caps: subtitles.Capabilities{Episodes: true}}))

	movieProviders := r.For(common.MediaKindMovie)
	require.Len(t, movieProviders, 1)
	assert.Equal(t, "opensubtitlescom", movieProviders[0].Name())

	episodeProviders := r.For(common.MediaKindEpisode)
	assert.Len(t, episodeProviders, 2)
}
