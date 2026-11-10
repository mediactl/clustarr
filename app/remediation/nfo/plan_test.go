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

package nfo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/nfo"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

var (
	now  = time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	item = schema.ItemRef{Kind: "Album", Ref: schema.Ref{Namespace: "media", Name: "muse-absolution", UID: "u1"}}
)

func rendered(doc string, images ...schema.NFOImage) *nfo.Rendered {
	files := []schema.NFOFile{{Name: "album.nfo", Content: []byte(doc)}}
	return &nfo.Rendered{Files: files, Images: images, Hash: nfo.HashOf(files, images)}
}

func base() nfo.Input {
	return nfo.Input{Item: item, Folder: "/data/music/Muse/Absolution", Enabled: true, Rendered: rendered("<album/>"), Now: now}
}

func answered(req *schema.NFOWriteTask, files []string, at time.Time) *schema.NFORecord {
	rec := nfo.Record(req)
	rec.State = records.StateAnswered
	t := at
	rec.AnsweredAt = &t
	rec.Answer = &schema.NFOWriteAnswer{Files: files, WrittenAt: at}
	return rec
}

func TestNothingToDoWithoutFolderMetadataOrLeave(t *testing.T) {
	for name, mutate := range map[string]func(*nfo.Input){
		"disabled":  func(in *nfo.Input) { in.Enabled = false },
		"no folder": func(in *nfo.Input) { in.Folder = "" },
		"no render": func(in *nfo.Input) { in.Rendered = nil },
	} {
		t.Run(name, func(t *testing.T) {
			in := base()
			mutate(&in)
			assert.Equal(t, nfo.Decision{}, nfo.Plan(in))
		})
	}
}

func TestAFreshItemIsRequestedOnce(t *testing.T) {
	in := base()
	d := nfo.Plan(in)
	require.NotNil(t, d.Request)
	assert.Equal(t, in.Rendered.Hash, d.Request.Hash)
	assert.Equal(t, in.Folder, d.Request.Folder)
	assert.Equal(t, now.Unix(), d.Request.Seq)
	assert.Empty(t, d.Request.Owned, "a first write owns nothing yet")
	assert.Nil(t, d.Status)
	assert.False(t, d.Answered)

	// The request outstanding and young: wait, no second request.
	in.Record, in.RecordOK = nfo.Record(d.Request), true
	in.Record.RequestedAt = now
	in.Now = now.Add(time.Minute)
	d2 := nfo.Plan(in)
	assert.Nil(t, d2.Request)
	assert.Equal(t, now.Add(nfo.RequestTimeout), d2.Due)

	// Outstanding past the timeout: asked again under a higher seq.
	in.Now = now.Add(nfo.RequestTimeout + time.Second)
	d3 := nfo.Plan(in)
	require.NotNil(t, d3.Request)
	assert.Greater(t, d3.Request.Seq, d.Request.Seq)
}

func TestAnAnswerIsIncorporatedAndReleasesTheDispatch(t *testing.T) {
	in := base()
	req := nfo.Plan(in).Request
	in.Record, in.RecordOK = answered(req, []string{"album.nfo", "folder.jpg"}, now.Add(time.Minute)), true
	in.Now = now.Add(2 * time.Minute)
	d := nfo.Plan(in)
	require.NotNil(t, d.Status)
	assert.Equal(t, in.Rendered.Hash, d.Status.Hash)
	assert.Equal(t, in.Folder, d.Status.Path)
	assert.Equal(t, []string{"album.nfo", "folder.jpg"}, d.Status.Files)
	require.NotNil(t, d.Status.WrittenAt)
	assert.True(t, d.Answered)
	assert.Nil(t, d.Request, "the rendering the answer wrote is current")

	// Incorporated: the next pass with the same rendering does nothing.
	in.Prev = d.Status
	d2 := nfo.Plan(in)
	assert.Nil(t, d2.Status)
	assert.Nil(t, d2.Request)
	assert.True(t, d2.Answered, "the ledger is told again until the answer's seq is released")
}

func TestAChangedRenderingIsWrittenAgainOverTheOwnedNames(t *testing.T) {
	in := base()
	in.Prev = &catalogv1alpha1.NFOStatus{Hash: "old", Path: in.Folder, Files: []string{"album.nfo", "folder.jpg"}}
	req := nfo.Plan(in).Request
	in.Record, in.RecordOK = answered(req, []string{"album.nfo", "folder.jpg"}, now), true
	in.Prev = &catalogv1alpha1.NFOStatus{Hash: req.Hash, Path: in.Folder, Files: []string{"album.nfo", "folder.jpg"}}
	in.Rendered = rendered("<album><title>Absolution</title></album>")
	in.Now = now.Add(time.Hour)
	d := nfo.Plan(in)
	require.NotNil(t, d.Request)
	assert.Equal(t, []string{"album.nfo", "folder.jpg"}, d.Request.Owned, "the agent may replace what it wrote before")
	assert.Equal(t, in.Rendered.Hash, d.Request.Hash)
	assert.Greater(t, d.Request.Seq, req.Seq)
}

func TestAMovedFolderOwnsNothingThere(t *testing.T) {
	in := base()
	in.Prev = &catalogv1alpha1.NFOStatus{Hash: in.Rendered.Hash, Path: "/data/music/Muse/Absolution (2003)", Files: []string{"album.nfo"}}
	d := nfo.Plan(in)
	require.NotNil(t, d.Request, "the same document at a new folder is written there")
	assert.Empty(t, d.Request.Owned)
}

func TestAFailureWaitsThenRetries(t *testing.T) {
	in := base()
	req := nfo.Plan(in).Request
	rec := nfo.Record(req)
	rec.State, rec.Failure, rec.Transient = records.StateFailed, "nfo: write: disk full", true
	at := now.Add(time.Minute)
	rec.AnsweredAt = &at
	in.Record, in.RecordOK = rec, true
	in.Now = at.Add(time.Minute)
	d := nfo.Plan(in)
	assert.True(t, d.Answered)
	assert.Nil(t, d.Request)
	assert.Equal(t, at.Add(nfo.RequestTimeout), d.Due)
	in.Now = at.Add(nfo.RequestTimeout)
	d2 := nfo.Plan(in)
	require.NotNil(t, d2.Request)
	assert.Greater(t, d2.Request.Seq, req.Seq)
}

func TestAnAnswerForAStaleRenderingRequestsTheNewOne(t *testing.T) {
	in := base()
	req := nfo.Plan(in).Request
	in.Record, in.RecordOK = answered(req, []string{"album.nfo"}, now), true
	in.Rendered = rendered("<album><review>new</review></album>")
	d := nfo.Plan(in)
	require.NotNil(t, d.Status, "the stale answer is still what is on disk")
	assert.Equal(t, req.Hash, d.Status.Hash)
	require.NotNil(t, d.Request, "and the new rendering is asked for")
	assert.Equal(t, in.Rendered.Hash, d.Request.Hash)
	assert.Equal(t, []string{"album.nfo"}, d.Request.Owned)
}
