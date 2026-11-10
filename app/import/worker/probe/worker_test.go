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

package probe

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// recordingProber is a mediainfo.Prober that answers every video probe with
// one MediaInfo and remembers the paths it was asked for: a file FFmpeg must
// never see is one it was never asked for.
type recordingProber struct {
	asked []string
	mi    commonv1.MediaInfo
}

func (p *recordingProber) Probe(_ context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	p.asked = append(p.asked, path)
	mi := p.mi
	return &mi, nil, nil
}

func (*recordingProber) ProbeAudio(context.Context, string) (mediainfo.AudioProbe, error) {
	return mediainfo.AudioProbe{}, errors.New("not faked")
}

// twoPageCBZ is a comic archive of two page images and a ComicInfo.xml.
func twoPageCBZ(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"ComicInfo.xml", "001.jpg", "002.jpg"} {
		f, err := zw.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte("x"))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// twoPagePDF declares two page objects beside the pages tree.
const twoPagePDF = "%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Type /Pages /Count 2 >> endobj\n" +
	"3 0 obj << /Type /Page >> endobj\n4 0 obj << /Type /Page >> endobj\n%%EOF\n"

// TestAnEbookProbesWithoutFFmpeg (2026-11-10 design §6; plan W3.1's
// leftover): a book file FFmpeg cannot read -- the extensions pkg/fsops
// classifies as book -- is answered before FFmpeg with its container alone,
// no stream and no page count (an EPUB is not parsed), as a comic archive
// or a PDF is answered with its page count; a video still goes to the
// prober. Until then the probe fell to FFmpeg and failed, holding every
// such Book at ProbePending.
func TestAnEbookProbesWithoutFFmpeg(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name   string
		bytes  []byte
		want   commonv1.MediaInfo
		ffmpeg bool
	}{
		{name: "book.epub", bytes: []byte("PK\x03\x04 not really a zip"), want: commonv1.MediaInfo{Container: "epub"}},
		{name: "book.MOBI", bytes: []byte("BOOKMOBI"), want: commonv1.MediaInfo{Container: "mobi"}},
		{name: "book.azw3", bytes: []byte("TPZ"), want: commonv1.MediaInfo{Container: "azw3"}},
		{name: "book.azw", bytes: []byte("TPZ"), want: commonv1.MediaInfo{Container: "azw"}},
		{name: "pages.pdf", bytes: []byte(twoPagePDF), want: commonv1.MediaInfo{Container: "pdf", PageCount: 2}},
		{name: "pages.cbz", bytes: twoPageCBZ(t), want: commonv1.MediaInfo{Container: "cbz", PageCount: 2}},
		{name: "clip.mkv", bytes: []byte("matroska"), want: commonv1.MediaInfo{Container: "mkv", VideoCodec: "hevc"}, ffmpeg: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			require.NoError(t, os.WriteFile(path, tc.bytes, 0o644))
			info, err := os.Stat(path)
			require.NoError(t, err)
			prober := &recordingProber{mi: commonv1.MediaInfo{Container: "mkv", VideoCodec: "hevc"}}
			w := &Worker{Prober: prober, Opts: Options{Pod: "probe-0", DataRoot: dir}}

			rec, outcome := w.probe(t.Context(), schema.ProbeTask{
				MediaFile: schema.Ref{Namespace: "media", Name: "file", UID: "uid-1"}, Path: path, ProbeVersion: mediainfo.ProbeVersion, Lane: "high",
			})
			assert.Equal(t, outcomeProbed, outcome)
			assert.Equal(t, schema.ProbeProbed, rec.State, rec.Failure)
			require.NotNil(t, rec.MediaInfo)
			assert.Equal(t, tc.want, *rec.MediaInfo)
			assert.Equal(t, mediainfo.ProbeHash(path, info.Size(), info.ModTime()), rec.ProbeHash)
			assert.Equal(t, mediainfo.ProbeVersion, rec.ProbeVersion)
			assert.False(t, rec.ProbedAt.IsZero())
			if tc.ffmpeg {
				assert.Equal(t, []string{path}, prober.asked, "a video is FFmpeg's")
			} else {
				assert.Empty(t, prober.asked, "FFmpeg is never asked about a book or a comic")
			}
		})
	}
}

// TestAnEbooksAnswerIsIncorporatedAndNamesTheBook follows the record
// downstream: the loop's probe planner (app/remediation/probe over
// mediafile.PlanProbe) incorporates the answer as it incorporates any
// probe -- Probed True, status.mediaInfo the record's -- and the naming
// planner, which holds a Book at ProbePending until it is probed, then
// renders the book's canonical path from it.
func TestAnEbooksAnswerIsIncorporatedAndNamesTheBook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Pratchett - Guards Guards.epub")
	require.NoError(t, os.WriteFile(path, []byte("PK\x03\x04"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "guards-file", UID: "uid-guards"},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards"}, Path: path},
	}
	task := schema.ProbeTask{
		MediaFile: schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)}, Path: path,
		ProbeHash: mediainfo.ProbeHash(path, info.Size(), info.ModTime()), ProbeVersion: mediainfo.ProbeVersion, Seq: 1, Lane: "high",
	}
	w := &Worker{Prober: &recordingProber{}, Opts: Options{Pod: "probe-0", DataRoot: dir}}
	rec, _ := w.probe(t.Context(), task)
	require.Equal(t, schema.ProbeProbed, rec.State, rec.Failure)
	// Store.Answer stamps the task's header onto the answer it writes.
	rec.MediaFile, rec.Seq, rec.RequestedVersion, rec.Lane = task.MediaFile, task.Seq, task.ProbeVersion, task.Lane

	now := metav1.NewTime(time.Now())
	out := &catalogv1alpha1.MediaFileStatus{}
	o := mediafile.PlanProbe(mediafile.ProbeInput{
		File: mf, Prev: &catalogv1alpha1.MediaFileStatus{}, Path: path, Info: info,
		Record: probestore.Current{Record: rec, OK: true, Revision: 1}, Read: true, Now: now,
	}, out)
	assert.Equal(t, []string{schema.ProbeProbed}, o.Records, "the record is incorporated")
	probed := k8s.FindCondition(out.Conditions, catalogv1alpha1.MediaFileConditionProbed)
	require.NotNil(t, probed)
	assert.Equal(t, metav1.ConditionTrue, probed.Status)
	require.NotNil(t, out.MediaInfo)
	assert.Equal(t, commonv1.MediaInfo{Container: "epub"}, *out.MediaInfo)

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "books"},
			Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/books", Kind: catalogv1alpha1.RootFolderKindBook},
		},
		&catalogv1alpha1.Author{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "pratchett"},
			Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL1A", RootFolderRef: "books"},
			Status:     catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: "Terry Pratchett"}},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "guards"},
			Spec:       catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "OL1W"},
			Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Guards! Guards!"}},
		},
	).Build()
	// A failed lookup renders nil (RenderNaming keeps a nil proposal), so
	// NotNil below is the lookup's own check.
	look := mediafile.LoadNaming(t.Context(), c, mf)
	n, _ := mediafile.RenderNaming(mf, look, nil, nil, path, false, false)
	require.NotNil(t, n)
	assert.Equal(t, catalogv1alpha1.NamingReasonProbePending, n.Reason, "unprobed, the book is held")
	n, _ = mediafile.RenderNaming(mf, look, out.MediaInfo, nil, path, false, false)
	require.NotNil(t, n)
	assert.Empty(t, n.Reason)
	assert.Equal(t, "/data/books/Terry Pratchett/Guards! Guards!/Terry Pratchett.epub", n.ExpectedPath)
}
