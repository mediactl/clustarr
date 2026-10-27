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

package usenet_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The client is asked for a transfer under the entry's id and told the
// release's title, which names a lone obfuscated file at publish: named
// for the id instead, 86's S01E13 parsed as absolute episode 50 and was
// held for a person (2026-10-27).
func TestAnAddCarriesTheReleaseTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`))
	}))
	defer srv.Close()
	fc := newFakeDownloadClient()
	jc := &journaledClient{fakeDownloadClient: fc, journals: map[string][]byte{}}
	tr := &usenetengine.Transfers{Client: jc, Resolver: &usenetengine.Resolver{}}
	url := srv.URL
	const title = "86.Eighty.Six.S01E13.2021.1080p.WEB-DL.AVC.AAC.2.0.Dual.Audio.-ZR"
	cmd := schema.EngineCommand{
		Seq: 1, Desired: schema.EngineDesiredPresent,
		Entry:   schema.EntryRef{ID: "86-eighty-six-cb83c58857-50e5c90a59", UID: "uid-e13"},
		Owner:   schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "86-eighty-six-cb83c58857"}},
		Source:  &commonv1.DownloadSource{NZBURL: &url},
		Release: schema.CommandRelease{Title: title},
	}
	_, err := tr.Apply(t.Context(), cmd, nil)
	require.NoError(t, err)
	require.Len(t, fc.addCalls, 1)
	require.Equal(t, "86-eighty-six-cb83c58857-50e5c90a59", fc.addCalls[0].Name)
	require.Equal(t, title, fc.addCalls[0].Title)
}
