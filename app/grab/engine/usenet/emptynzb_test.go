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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
	"github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// An NZB with no articles can never download: nzb-life served three Naked
// and Afraid releases that way (2026-10-21), the engine retried each as
// "cannot apply the command yet", and every retry was another download to
// nzb-life, which then refused the release for 24 h -- five days running.
// Such a payload is the release's fault, payloadUnavailable, which the
// manager blocklists and searches past.
func TestAnNZBWithNoArticlesIsThePayloadUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`))
	}))
	defer srv.Close()
	fc := newFakeDownloadClient()
	fc.addErr = fmt.Errorf("usenet: add: %w", usenet.ErrEmptyNZB)
	tr := &usenetengine.Transfers{Client: fc, Resolver: &usenetengine.Resolver{}}
	url := srv.URL
	cmd := schema.EngineCommand{
		Seq: 3, Desired: schema.EngineDesiredPresent, Entry: schema.EntryRef{ID: "naked-and-afraid-e06", UID: "uid-e06"},
		Owner:  schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "naked-and-afraid"}},
		Source: &commonv1.DownloadSource{NZBURL: &url},
	}
	_, err := tr.Apply(t.Context(), cmd, nil)
	require.ErrorIs(t, err, engine.ErrPayloadUnavailable)
	require.ErrorIs(t, err, usenet.ErrEmptyNZB)
}
