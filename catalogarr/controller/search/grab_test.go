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
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestBuildDownloadSource(t *testing.T) {
	magnet := BuildDownloadSource(commonv1.ReleaseInfo{
		GUID: "g1", IndexerRef: "idx",
		MagnetURL:   "magnet:?xt=urn:btih:abc",
		DownloadURL: "https://idx.example/dl/g1",
	})
	require.NotNil(t, magnet.MagnetURL)
	require.Equal(t, "magnet:?xt=urn:btih:abc", *magnet.MagnetURL)
	require.Nil(t, magnet.IndexerDownload, "IndexerDownload set alongside MagnetURL violates the source CEL rule")

	indexed := BuildDownloadSource(commonv1.ReleaseInfo{
		GUID: "g2", IndexerRef: "idx", DownloadURL: "https://idx.example/dl/g2",
	})
	require.Nil(t, indexed.MagnetURL)
	require.NotNil(t, indexed.IndexerDownload)
	require.Equal(t, "g2", indexed.IndexerDownload.GUID)
	require.Equal(t, "idx", indexed.IndexerDownload.IndexerRef)
	require.Equal(t, "https://idx.example/dl/g2", indexed.IndexerDownload.URL)
}

func TestToDownloadSourceACMapsBothBranches(t *testing.T) {
	magnet := toDownloadSourceAC(BuildDownloadSource(commonv1.ReleaseInfo{MagnetURL: "magnet:?xt=urn:btih:abc"}))
	require.NotNil(t, magnet.MagnetURL)
	require.Equal(t, "magnet:?xt=urn:btih:abc", *magnet.MagnetURL)
	require.Nil(t, magnet.IndexerDownload)

	indexed := toDownloadSourceAC(BuildDownloadSource(commonv1.ReleaseInfo{
		GUID: "g2", IndexerRef: "idx", DownloadURL: "https://idx.example/dl/g2",
	}))
	require.Nil(t, indexed.MagnetURL)
	require.NotNil(t, indexed.IndexerDownload)
	require.Equal(t, "g2", *indexed.IndexerDownload.GUID)
	require.Equal(t, "idx", *indexed.IndexerDownload.IndexerRef)
	require.Equal(t, "https://idx.example/dl/g2", *indexed.IndexerDownload.URL)
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
