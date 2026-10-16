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
package ui

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/views"
)

// A movie posted in parts is one search result, which says how many parts
// its download fetches and joins.
func TestASetsSearchResultSaysHowManyPartsItFetches(t *testing.T) {
	rd := commonv1.ReleaseDecision{ReleaseInfo: commonv1.ReleaseInfo{
		GUID: "parts:abc", Title: "56.Up.2012.1080p.BluRay.x264-USURY", Protocol: commonv1.ProtocolUsenet,
		Parts: []commonv1.ReleasePart{{Part: 1, GUID: "a"}, {Part: 2, GUID: "b"}, {Part: 3, GUID: "c"}},
	}, Approved: true}
	row := releaseRow(rd, time.Now())
	assert.Equal(t, 3, row.Parts)
	assert.Zero(t, releaseRow(commonv1.ReleaseDecision{}, time.Now()).Parts)

	var buf bytes.Buffer
	require.NoError(t, views.SearchPanelView(views.SearchPanel{Results: []views.ReleaseRow{row}}).Render(context.Background(), &buf))
	assert.Contains(t, buf.String(), "3 parts")
}
