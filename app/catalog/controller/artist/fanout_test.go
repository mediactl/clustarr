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

package artist_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func defaultProfile() catalogv1alpha1.MusicMetadataProfile {
	return catalogv1alpha1.MusicMetadataProfile{
		PrimaryTypes:    []string{"album", "ep"},
		SecondaryTypes:  []string{"studio"},
		ReleaseStatuses: []string{"official"},
	}
}

func TestAlbumAcceptedPrimaryType(t *testing.T) {
	profile := defaultProfile()
	cases := []struct {
		name  string
		prime string
		want  bool
	}{
		{"album accepted", "Album", true},
		{"ep accepted", "EP", true},
		{"single rejected by default profile", "Single", false},
		{"broadcast rejected", "Broadcast", false},
		{"empty primary type rejected", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			alb := pkgmetadata.Album{PrimaryType: c.prime}
			assert.Equal(t, c.want, artist.AlbumAccepted(profile, alb))
		})
	}
}

func TestAlbumAcceptedSecondaryTypesEmptyMeansStudio(t *testing.T) {
	profile := defaultProfile() // SecondaryTypes: {studio}
	alb := pkgmetadata.Album{PrimaryType: "Album"}
	assert.True(t, artist.AlbumAccepted(profile, alb), "no secondary types folds onto the profile's studio default")
}

// TestAlbumAcceptedSecondaryTypesFollowLidarr pins Lidarr's rule
// (SkyHookProxy.FilterAlbums, SkyHookProxy.cs lines 144-146 at da7b4dfb):
// no secondary types passes when Studio is allowed, and otherwise ANY one
// allowed secondary type is enough.
func TestAlbumAcceptedSecondaryTypesFollowLidarr(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		types   []string
		want    bool
	}{
		{"no types, studio allowed", []string{"studio"}, nil, true},
		{"no types, studio not allowed", []string{"compilation"}, nil, false},
		{"one allowed among disallowed is enough", []string{"studio", "compilation"}, []string{"Live", "Compilation"}, true},
		{"every type allowed", []string{"compilation", "live"}, []string{"Live", "Compilation"}, true},
		{"no type allowed", []string{"studio"}, []string{"Live", "Compilation"}, false},
		{"studio does not admit an album that has types", []string{"studio"}, []string{"Live"}, false},
		{"an unfoldable type counts as not allowed", []string{"live"}, []string{"Unknown new type"}, false},
		{"an unfoldable type beside an allowed one", []string{"live"}, []string{"Unknown new type", "Live"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			profile := defaultProfile()
			profile.SecondaryTypes = c.allowed
			alb := pkgmetadata.Album{PrimaryType: "Album", SecondaryTypes: c.types}
			assert.Equal(t, c.want, artist.AlbumAccepted(profile, alb))
		})
	}
}

func TestAlbumAcceptedSecondaryTypeCrosswalk(t *testing.T) {
	// Pins the three secondary types whose CRD token does not fold from a
	// plain lower-case of the MusicBrainz string (docs/research/metadata.md
	// :253-254): "Audio drama" (space), "DJ-mix" (hyphen+case), and
	// "Mixtape/Street" (slash, and a different CRD spelling entirely).
	cases := []struct {
		mb    string
		token string
	}{
		{"Audio drama", "audioDrama"},
		{"DJ-mix", "djMix"},
		{"Mixtape/Street", "mixtape"},
	}
	for _, c := range cases {
		t.Run(c.mb, func(t *testing.T) {
			profile := catalogv1alpha1.MusicMetadataProfile{
				PrimaryTypes:    []string{"album"},
				SecondaryTypes:  []string{c.token},
				ReleaseStatuses: []string{"official"},
			}
			alb := pkgmetadata.Album{PrimaryType: "Album", SecondaryTypes: []string{c.mb}}
			assert.True(t, artist.AlbumAccepted(profile, alb), "%s should fold onto %s", c.mb, c.token)
		})
	}
}

func TestAlbumAcceptedFieldRecording(t *testing.T) {
	// MusicBrainz's "Field recording" (https://musicbrainz.org/doc/Release_Group/Type)
	// folds onto the CRD token fieldRecording: accepted when the profile
	// lists it, rejected when it does not.
	profile := catalogv1alpha1.MusicMetadataProfile{
		PrimaryTypes:    []string{"album"},
		SecondaryTypes:  []string{"fieldRecording"},
		ReleaseStatuses: []string{"official"},
	}
	alb := pkgmetadata.Album{PrimaryType: "Album", SecondaryTypes: []string{"Field recording"}}
	assert.True(t, artist.AlbumAccepted(profile, alb))

	profile.SecondaryTypes = []string{"studio", "compilation", "soundtrack", "spokenword", "interview", "audiobook", "live", "remix", "djMix", "mixtape", "demo", "audioDrama"}
	assert.False(t, artist.AlbumAccepted(profile, alb), "every other token listed, but not fieldRecording")
}

func TestAlbumAcceptedReleaseStatuses(t *testing.T) {
	profile := defaultProfile() // ReleaseStatuses: {official}
	official := pkgmetadata.Album{PrimaryType: "Album", Releases: []pkgmetadata.AlbumRelease{{Status: "official"}}}
	bootleg := pkgmetadata.Album{PrimaryType: "Album", Releases: []pkgmetadata.AlbumRelease{{Status: "bootleg"}}}
	pseudo := pkgmetadata.Album{PrimaryType: "Album", Releases: []pkgmetadata.AlbumRelease{{Status: "pseudo-release"}}}
	mixed := pkgmetadata.Album{PrimaryType: "Album", Releases: []pkgmetadata.AlbumRelease{{Status: "bootleg"}, {Status: "official"}}}
	noReleases := pkgmetadata.Album{PrimaryType: "Album"}

	assert.True(t, artist.AlbumAccepted(profile, official))
	assert.False(t, artist.AlbumAccepted(profile, bootleg))
	assert.True(t, artist.AlbumAccepted(profile, mixed), "at least one accepted release status is enough")
	assert.True(t, artist.AlbumAccepted(profile, noReleases), "no release data to check against passes rather than blocking on a gap this task did not create")

	pseudoProfile := defaultProfile()
	pseudoProfile.ReleaseStatuses = []string{"pseudoRelease"}
	assert.True(t, artist.AlbumAccepted(pseudoProfile, pseudo), "pseudo-release folds onto pseudoRelease")
}

// TestReleaseStatusAcceptedFoldsMusicBrainzSpelling pins the spelling the
// web service actually sends ("Official", "Pseudo-Release"; see
// test/data/metadata/musicbrainz/browse_releases_the_bends.json), which an
// exact-case table matched none of.
func TestReleaseStatusAcceptedFoldsMusicBrainzSpelling(t *testing.T) {
	profile := catalogv1alpha1.MusicMetadataProfile{ReleaseStatuses: []string{"official", "pseudoRelease"}}
	for _, c := range []struct {
		status string
		want   bool
	}{
		{"Official", true},
		{"official", true},
		{"Pseudo-Release", true},
		{"Promotion", false},
		{"Bootleg", false},
		{"Withdrawn", false},
		{"Cancelled", false},
		{"", false},
	} {
		assert.Equal(t, c.want, artist.ReleaseStatusAccepted(profile, c.status), "status %q", c.status)
	}
	official := pkgmetadata.Album{PrimaryType: "Album", Releases: []pkgmetadata.AlbumRelease{{Status: "Official"}}}
	assert.True(t, artist.AlbumAccepted(defaultProfile(), official), "a real MusicBrainz release list must pass an official-only profile")
}

func TestInitialAlbumMonitored(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	past := now.AddDate(-1, 0, 0)
	future := now.AddDate(1, 0, 0)

	all := []artist.AlbumCandidate{
		{ReleaseGroupID: "first", ReleaseDate: &past},
		{ReleaseGroupID: "last", ReleaseDate: &future},
	}

	cases := []struct {
		name string
		mode catalogv1alpha1.ArtistMonitorMode
		alb  artist.AlbumCandidate
		want bool
	}{
		{"all monitors everything", catalogv1alpha1.ArtistMonitorAll, artist.AlbumCandidate{ReleaseGroupID: "first", ReleaseDate: &past}, true},
		{"future monitors unreleased", catalogv1alpha1.ArtistMonitorFuture, artist.AlbumCandidate{ReleaseGroupID: "last", ReleaseDate: &future}, true},
		{"future skips released", catalogv1alpha1.ArtistMonitorFuture, artist.AlbumCandidate{ReleaseGroupID: "first", ReleaseDate: &past}, false},
		{"missing monitors released", catalogv1alpha1.ArtistMonitorMissing, artist.AlbumCandidate{ReleaseGroupID: "first", ReleaseDate: &past}, true},
		{"missing skips unreleased", catalogv1alpha1.ArtistMonitorMissing, artist.AlbumCandidate{ReleaseGroupID: "last", ReleaseDate: &future}, false},
		{"existing never monitors at add", catalogv1alpha1.ArtistMonitorExisting, artist.AlbumCandidate{ReleaseGroupID: "first", ReleaseDate: &past}, false},
		{"latest monitors only the latest-dated album", catalogv1alpha1.ArtistMonitorLatest, artist.AlbumCandidate{ReleaseGroupID: "last", ReleaseDate: &future}, true},
		{"latest skips the earlier one", catalogv1alpha1.ArtistMonitorLatest, artist.AlbumCandidate{ReleaseGroupID: "first", ReleaseDate: &past}, false},
		{"first monitors only the earliest-dated album", catalogv1alpha1.ArtistMonitorFirst, artist.AlbumCandidate{ReleaseGroupID: "first", ReleaseDate: &past}, true},
		{"first skips the later one", catalogv1alpha1.ArtistMonitorFirst, artist.AlbumCandidate{ReleaseGroupID: "last", ReleaseDate: &future}, false},
		{"none never monitors", catalogv1alpha1.ArtistMonitorNone, artist.AlbumCandidate{ReleaseGroupID: "first", ReleaseDate: &past}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := artist.InitialAlbumMonitored(c.mode, c.alb, all, catalogv1alpha1.ArtistRunStatusContinuing, now)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestInitialAlbumMonitoredFutureRespectsEndedArtist(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.AddDate(1, 0, 0)
	alb := artist.AlbumCandidate{ReleaseGroupID: "x", ReleaseDate: &future}
	assert.True(t, artist.InitialAlbumMonitored(catalogv1alpha1.ArtistMonitorFuture, alb, nil, catalogv1alpha1.ArtistRunStatusContinuing, now))
	assert.False(t, artist.InitialAlbumMonitored(catalogv1alpha1.ArtistMonitorFuture, alb, nil, catalogv1alpha1.ArtistRunStatusEnded, now),
		"an ended artist has no future to monitor toward, mirroring series' identical Future/ended check")
}

func TestAlbumName(t *testing.T) {
	name1 := artist.AlbumName("Radiohead", "f5093c06-23e3-404f-aeaa-40f72885ee3a")
	name2 := artist.AlbumName("Radiohead", "f5093c06-23e3-404f-aeaa-40f72885ee3a")
	name3 := artist.AlbumName("Radiohead", "a-different-release-group-id")

	assert.Equal(t, name1, name2, "deterministic: same inputs, same name")
	assert.NotEqual(t, name1, name3, "different release groups never collide")
	assert.Regexp(t, `^radiohead-[0-9a-f]{8}$`, name1, "<artist>-<releasegroup-uid8> per design §4.2")
}

func TestDesiredAlbumsAppliesMetadataProfileFilter(t *testing.T) {
	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "media"},
		Spec: catalogv1alpha1.ArtistSpec{
			MetadataProfile: defaultProfile(), // {album,ep} / {studio} / {official}
			AddOptions:      catalogv1alpha1.ArtistAddOptions{Monitor: catalogv1alpha1.ArtistMonitorAll},
		},
	}
	albums := []pkgmetadata.Album{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "ok-computer"}, PrimaryType: "Album"},
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "live-bootleg"}, PrimaryType: "Album", SecondaryTypes: []string{"Live"}},
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "some-single"}, PrimaryType: "Single"},
	}

	desired := artist.DesiredAlbums(a, false, nil, albums, time.Now())

	require.Len(t, desired, 1, "only the plain studio album passes the default profile")
	assert.Equal(t, "ok-computer", desired[0].ReleaseGroupID)
	require.NotNil(t, desired[0].Monitored)
	assert.True(t, *desired[0].Monitored)
}

func TestDesiredAlbumsMonitoredOnlyAtCreation(t *testing.T) {
	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "media"},
		Spec: catalogv1alpha1.ArtistSpec{
			MetadataProfile: defaultProfile(),
			MonitorNewItems: catalogv1alpha1.MonitorNewItemsAll,
			AddOptions:      catalogv1alpha1.ArtistAddOptions{Monitor: catalogv1alpha1.ArtistMonitorNone},
		},
	}
	albums := []pkgmetadata.Album{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "ok-computer"}, PrimaryType: "Album"},
	}

	// addOptionsApplied=false, already exists: still an add-time decision
	// (monitor=None -> false), NOT a MonitorNewItems decision.
	existing := map[string]bool{"ok-computer": true}
	desired := artist.DesiredAlbums(a, false, existing, albums, time.Now())
	require.Len(t, desired, 1)
	require.NotNil(t, desired[0].Monitored)
	assert.False(t, *desired[0].Monitored, "add-time AddOptions.Monitor governs while addOptionsApplied is false, regardless of existingReleaseGroups")

	// addOptionsApplied=true, already exists: nil -- never touch an
	// already-created Album's spec.monitored again.
	desired = artist.DesiredAlbums(a, true, existing, albums, time.Now())
	require.Len(t, desired, 1)
	assert.Nil(t, desired[0].Monitored)

	// addOptionsApplied=true, NOT yet existing (a brand-new release
	// discovered on a later refresh): governed by spec.monitorNewItems.
	desired = artist.DesiredAlbums(a, true, nil, albums, time.Now())
	require.Len(t, desired, 1)
	require.NotNil(t, desired[0].Monitored)
	assert.True(t, *desired[0].Monitored)
}

func TestDesiredAlbumsMonitorNewItemsNew(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	past := now.AddDate(-1, 0, 0)
	future := now.AddDate(1, 0, 0)
	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "media"},
		Spec: catalogv1alpha1.ArtistSpec{
			MetadataProfile: defaultProfile(),
			MonitorNewItems: catalogv1alpha1.MonitorNewItemsNew,
		},
	}
	albums := []pkgmetadata.Album{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "unreleased"}, PrimaryType: "Album", ReleaseDate: &future},
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "already-out"}, PrimaryType: "Album", ReleaseDate: &past},
	}

	desired := artist.DesiredAlbums(a, true, nil, albums, now)
	require.Len(t, desired, 2)
	byID := map[string]*bool{}
	for _, d := range desired {
		byID[d.ReleaseGroupID] = d.Monitored
	}
	require.NotNil(t, byID["unreleased"])
	assert.True(t, *byID["unreleased"], "MonitorNewItemsNew monitors an album that has not released yet")
	require.NotNil(t, byID["already-out"])
	assert.False(t, *byID["already-out"], "MonitorNewItemsNew does not retroactively monitor an already-released back-catalog item")
}

func TestDesiredAlbumsSkipsAlbumsWithNoReleaseGroupID(t *testing.T) {
	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "media"},
		Spec:       catalogv1alpha1.ArtistSpec{MetadataProfile: defaultProfile()},
	}
	albums := []pkgmetadata.Album{{PrimaryType: "Album"}} // no IDs at all
	desired := artist.DesiredAlbums(a, false, nil, albums, time.Now())
	assert.Empty(t, desired)
}
