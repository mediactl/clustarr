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

package search

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab/downloads"
)

// TestBuildDownloadSourceIsTheGrabWorkersMapping pins the interactive path to
// downloads.ResolveSource, the mapping the automatic grab path applies. The
// two name a Download identically and spec.source is immutable, so any
// release they map differently is a release a user's grab and an automatic
// grab cannot both apply -- the carried "two grab paths disagree" failure.
func TestBuildDownloadSourceIsTheGrabWorkersMapping(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	for _, rel := range []commonv1.ReleaseInfo{
		{
			Protocol: commonv1.ProtocolTorrent, GUID: "g1", IndexerRef: "idx",
			MagnetURL: "magnet:?xt=urn:btih:abc", DownloadURL: "https://idx.example/dl/g1", InfoHash: hash,
		},
		{
			Protocol: commonv1.ProtocolTorrent, GUID: "g2", IndexerRef: "idx",
			DownloadURL: "https://idx.example/dl/g2", InfoHash: strings.ToUpper(hash),
		},
		{Protocol: commonv1.ProtocolUsenet, GUID: "g3", IndexerRef: "idx", DownloadURL: "https://idx.example/nzb/g3"},
		{Protocol: commonv1.ProtocolTorrent, GUID: "g4", DownloadURL: "https://tracker.example/g4.torrent"},
	} {
		want, err := downloads.ResolveSource(rel)
		require.NoError(t, err)
		require.Equalf(t, want, BuildDownloadSource(rel), "release %s", rel.GUID)
		require.Equalf(t, downloads.SourceApplyConfiguration(want), toDownloadSourceAC(BuildDownloadSource(rel)),
			"release %s", rel.GUID)
	}

	magnet := BuildDownloadSource(commonv1.ReleaseInfo{
		Protocol: commonv1.ProtocolTorrent, GUID: "g1", IndexerRef: "idx",
		MagnetURL: "magnet:?xt=urn:btih:abc", InfoHash: hash,
	})
	require.NotNil(t, magnet.MagnetURL)
	require.Nil(t, magnet.IndexerDownload, "IndexerDownload set alongside MagnetURL violates the source CEL rule")
	require.NotNil(t, magnet.ExpectedInfoHash, "the info-hash guard rides along on the interactive path too")
	require.Equal(t, hash, *magnet.ExpectedInfoHash)

	require.Equal(t, downloadv1alpha1.DownloadSource{}, BuildDownloadSource(commonv1.ReleaseInfo{GUID: "nothing"}),
		"a release with no source maps to an empty one, which the apiserver refuses on apply")
}

func TestResolveGrab(t *testing.T) {
	permanent := commonv1.ReleaseDecision{
		ReleaseInfo: commonv1.ReleaseInfo{GUID: "perm"},
		Rejections:  []commonv1.Rejection{{Reason: "release group is unwanted", Type: commonv1.RejectionPermanent}},
	}
	temporary := commonv1.ReleaseDecision{
		ReleaseInfo:         commonv1.ReleaseInfo{GUID: "temp"},
		TemporarilyRejected: true,
		Rejections:          []commonv1.Rejection{{Reason: "queue already has an equal candidate", Type: commonv1.RejectionTemporary}},
	}
	ok := commonv1.ReleaseDecision{ReleaseInfo: commonv1.ReleaseInfo{GUID: "ok"}, Approved: true}
	results := []commonv1.ReleaseDecision{ok, permanent, temporary}

	tests := []struct {
		name        string
		guid        string
		override    bool
		wantAllowed bool
		wantErr     bool
	}{
		{name: "an approved release needs no override", guid: "ok", wantAllowed: true},
		{name: "a permanent rejection is refused without override", guid: "perm", wantErr: true},
		{name: "a permanent rejection is allowed with override", guid: "perm", override: true, wantAllowed: true},
		{name: "a temporary rejection needs no override", guid: "temp", wantAllowed: true},
		{name: "an unknown guid is an error, not a silent skip", guid: "nope", override: true, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveGrab(tc.guid, results, tc.override)
			require.Equal(t, tc.wantAllowed, got.Allowed)
			if tc.wantErr {
				require.NotEmpty(t, got.Error)
				return
			}
			require.Empty(t, got.Error)
			require.Equal(t, tc.guid, got.Release.GUID)
		})
	}
}
