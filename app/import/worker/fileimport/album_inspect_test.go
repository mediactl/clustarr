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

package fileimport_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// tagProber is a mediainfo.Prober whose ProbeAudio answers from a table by
// file name, as the import agent's in-process probe answers from a file's
// tags; a file the table does not name is an untagged 16-bit FLAC. Its
// Probe (video) fails, since nothing here is a video.
type tagProber map[string]mediainfo.AudioProbe

func (tagProber) Probe(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	return nil, nil, errors.New("not a video")
}

func (p tagProber) ProbeAudio(_ context.Context, path string) (mediainfo.AudioProbe, error) {
	if ap, ok := p[filepath.Base(path)]; ok {
		return ap, nil
	}
	return mediainfo.AudioProbe{Codec: "flac", SampleBits: 16, DurationMs: 180_000}, nil
}

func taggedTrack(disc, track int32) mediainfo.AudioProbe {
	return mediainfo.AudioProbe{Codec: "flac", SampleBits: 16, DurationMs: 180_000, Tags: &commonv1.MediaTags{
		Artist: "Muse", Album: "Absolution", DiscNumber: disc, DiscTotal: 2, TrackNumber: track,
	}}
}

// nonVideoProfile is a minimal, valid music or audiobook QualityProfile.
func nonVideoProfile(name string, kind catalogv1alpha1.ProfileMediaKind, cutoff string, tiers ...catalogv1alpha1.Tier) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       catalogv1alpha1.QualityProfileSpec{MediaKind: kind, Cutoff: cutoff, Tiers: tiers},
	}
}

// nonVideoRun drives one non-video entry's import: the inspect, its record,
// and the decision after it.
type nonVideoRun struct {
	*importRun
	root string
}

func newNonVideoRun(t *testing.T, w *fileimport.Worker, c client.Client, owner schema.ItemRef, kind commonv1.MediaKind,
	qp *catalogv1alpha1.QualityProfile, releaseTitle, root string, now time.Time,
) *nonVideoRun {
	t.Helper()
	prof, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	run := &importRun{t: t, w: w, bus: w.Bus, existing: c, in: importplan.Input{
		Owner: owner, OwnerKind: kind,
		Entry: catalogv1alpha1.DownloadEntry{
			ID: "dl-" + filepath.Base(root), UID: filepath.Base(root),
			Release:   catalogv1alpha1.DownloadRelease{Title: releaseTitle, Protocol: commonv1.ProtocolUsenet},
			GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
			Phase: commonv1.DownloadPhaseCompleted,
		},
		Transfer:   &schema.TransferRecord{ContentRoot: root},
		Targets:    []schema.ItemRef{owner},
		ProfileRef: qp.Name, Profile: &prof, Now: now,
	}}
	return &nonVideoRun{importRun: run, root: root}
}

// inspected plants the files under the content root, runs the inspect and
// returns its record's files by their path relative to the root.
func (r *nonVideoRun) inspected(t *testing.T, files ...string) map[string]schema.InspectedFile {
	t.Helper()
	for _, f := range files {
		mustWriteSparseFile(t, filepath.Join(r.root, f), 1<<20)
	}
	r.decide()
	r.deliver()
	rec := r.record(schema.ImportSubInspect)
	require.NotNil(t, rec)
	require.NotNil(t, rec.Inspect)
	require.Len(t, rec.Inspect.Files, len(files), "%+v", rec.Inspect)
	out := map[string]schema.InspectedFile{}
	for _, f := range rec.Inspect.Files {
		rel, err := filepath.Rel(r.root, f.Path)
		require.NoError(t, err)
		out[rel] = f
	}
	return out
}

// Every audio file of an album download is attributed to one track of the
// selected release (2026-11-10 design D6, §6): the tags' disc and track
// number first, the file's name (with its disc folder) second; a file
// naming no track is held for a person, and so is a second file for a track
// another file already holds, each with the reason. The attributed files
// still import, their track on the MediaFile's mediaRef.
func TestAnAlbumDownloadIsAttributedTrackByTrack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	library, downloads := filepath.Join(dir, "media", "music"), filepath.Join(dir, "downloads")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)

	qp := nonVideoProfile("lossless", catalogv1alpha1.ProfileMediaKindMusic, "lossless",
		catalogv1alpha1.Tier{Name: "lossless", Qualities: []string{"FLAC"}})
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "music", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindMusic},
	}
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "muse", Namespace: "media", UID: "muse-uid"},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "9c9f1980-dc94-4cd2-a64b-0c5a0a00f8e4", QualityProfileRef: qp.Name, RootFolderRef: rf.Name,
		},
		Status: catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Muse"}, Path: filepath.Join(library, "Muse")},
	}
	tracks := []catalogv1alpha1.Track{
		{RecordingID: "d1t1", Medium: 1, Number: 1, Title: "Intro"},
		{RecordingID: "d1t2", Medium: 1, Number: 2, Title: "Apocalypse Please"},
		{RecordingID: "d2t1", Medium: 2, Number: 1, Title: "Hysteria"},
		{RecordingID: "d2t2", Medium: 2, Number: 2, Title: "Blackout"},
	}
	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "muse-absolution", Namespace: "media", UID: "absolution-uid"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: artist.Name, ReleaseGroupID: "rg-absolution"},
		Status: catalogv1alpha1.AlbumStatus{
			Metadata: &catalogv1alpha1.AlbumMetadata{
				Title:             "Absolution",
				Releases:          []catalogv1alpha1.ReleaseSummary{{ID: "rel-1", Media: []catalogv1alpha1.Medium{{Number: 1}, {Number: 2}}}},
				SelectedReleaseID: "rel-1",
			},
			Tracks: tracks,
			Path:   filepath.Join(library, "Muse", "Absolution (2003)"),
		},
	}
	c, writes := readOnlyClient(t, qp, rf, artist, album)
	bus := newBus(t, ctx)
	prober := tagProber{
		"01 - Intro.flac":     taggedTrack(1, 1),
		"intro (alt).flac":    taggedTrack(1, 1),
		"02 - Blackout.flac":  {Codec: "flac", SampleBits: 16, DurationMs: 180_000}, // untagged: its name and folder decide
		"bonus.flac":          {Codec: "flac", SampleBits: 16, DurationMs: 180_000}, // untagged, unnumbered: nothing decides
		"something-else.flac": {Codec: "flac", SampleBits: 16, DurationMs: 180_000},
	}
	w := fileimport.NewWorker(c, bus, prober)
	w.Clock = func() time.Time { return now }
	owner := schema.ItemRef{Kind: "Album", Ref: schema.Ref{Namespace: "media", Name: album.Name, UID: string(album.UID)}}

	t.Run("tags, then name, then a person", func(t *testing.T) {
		r := newNonVideoRun(t, w, c, owner, commonv1.MediaKindAlbum, qp, "Muse-Absolution-2CD-FLAC", filepath.Join(downloads, "two-discs"), now)
		intro, alt, blackout, bonus := "01 - Intro.flac", filepath.Join("Disc 1", "intro (alt).flac"), filepath.Join("CD2", "02 - Blackout.flac"), "bonus.flac"
		files := r.inspected(t, intro, alt, blackout, bonus)

		// The tags name disc 1 track 1; the name says the same.
		require.Empty(t, files[intro].Rejections, "%+v", files[intro].Rejections)
		assert.Equal(t, "d1t1", files[intro].Frozen.Track)
		require.NotNil(t, files[intro].Proposed)
		assert.Equal(t, "Album", files[intro].Proposed.Kind)
		assert.Equal(t, album.Name, files[intro].Proposed.Name)
		assert.Equal(t, []string{album.Name}, files[intro].Keys)
		assert.Equal(t, filepath.Join(album.Status.Path, intro), files[intro].Dest, "a track keeps its relative path under the album's folder")
		require.NotNil(t, files[intro].Frozen.Quality)
		assert.Equal(t, "FLAC", files[intro].Frozen.Quality.Name, "frozen from the probe")

		// No tags: "02 - Blackout" is track 2, and the CD2 folder its disc.
		require.Empty(t, files[blackout].Rejections, "%+v", files[blackout].Rejections)
		assert.Equal(t, "d2t2", files[blackout].Frozen.Track)
		assert.Equal(t, filepath.Join(album.Status.Path, "CD2", "02 - Blackout.flac"), files[blackout].Dest)

		// A second file tagged disc 1 track 1 is not that track too.
		require.Len(t, files[alt].Rejections, 1, "%+v", files[alt])
		assert.Equal(t, string(commonv1.ImportClassNeedsPerson), files[alt].Rejections[0].Class)
		assert.Contains(t, files[alt].Rejections[0].Text, "are both track d1t1 of the selected release")
		assert.Contains(t, files[alt].Rejections[0].Text, "01 - Intro.flac")
		assert.Empty(t, files[alt].Frozen.Track)

		// Nothing names a track for the stray.
		require.Len(t, files[bonus].Rejections, 1, "%+v", files[bonus])
		assert.Equal(t, string(commonv1.ImportClassNeedsPerson), files[bonus].Rejections[0].Class)
		assert.Contains(t, files[bonus].Rejections[0].Text, "bonus.flac names no track of the selected release (4 tracks on 2 media)")
		assert.Empty(t, files[bonus].Frozen.Track)

		// The two attributed files import; the held ones are left behind.
		dec := r.decide()
		task := r.executeTask()
		require.Len(t, task.Plan.Moves, 2, "%+v", dec.Summary)
		moved := map[string]string{}
		for _, m := range task.Plan.Moves {
			moved[filepath.Base(m.Source)] = m.Frozen.Track
		}
		assert.Equal(t, map[string]string{"01 - Intro.flac": "d1t1", "02 - Blackout.flac": "d2t2"}, moved)
		assert.Contains(t, dec.Summary.Message, "left behind")

		// The execute writes the track onto the MediaFile's mediaRef.
		r.deliver()
		dec = r.decide()
		assert.Equal(t, lifecycle.ImportImported, dec.Verdict, "%+v", dec.Summary)
		refs := map[string]commonv1.MediaRef{}
		for _, m := range dec.Materialise {
			if m.Apply != nil {
				refs[filepath.Base(m.Apply.Path)] = m.Apply.Ref
			}
		}
		assert.Equal(t, map[string]commonv1.MediaRef{
			"01 - Intro.flac":    {Kind: commonv1.MediaKindAlbum, Name: album.Name, Track: "d1t1"},
			"02 - Blackout.flac": {Kind: commonv1.MediaKindAlbum, Name: album.Name, Track: "d2t2"},
		}, refs)
		assert.FileExists(t, filepath.Join(album.Status.Path, "CD2", "02 - Blackout.flac"))
		assert.FileExists(t, filepath.Join(r.root, "bonus.flac"), "a held file stays in the download")
	})

	t.Run("a download no file of which names a track is held whole", func(t *testing.T) {
		r := newNonVideoRun(t, w, c, owner, commonv1.MediaKindAlbum, qp, "Muse-Absolution-2CD-FLAC", filepath.Join(downloads, "strays"), now)
		files := r.inspected(t, "bonus.flac", "something-else.flac")
		for rel, f := range files {
			require.Len(t, f.Rejections, 1, rel)
			assert.Equal(t, string(commonv1.ImportClassNeedsPerson), f.Rejections[0].Class, rel)
		}
		dec := r.decide()
		assert.Nil(t, dec.Dispatch, "nothing to execute")
		assert.Equal(t, lifecycle.ImportHeld, dec.Verdict)
		assert.Equal(t, commonv1.ImportClassNeedsPerson, dec.Class)
		assert.Contains(t, dec.Summary.Message, "names no track of the selected release")
	})

	assert.Empty(t, writes(), "the import agent asked for Kubernetes writes")
}

// An audiobook's parts are ordered by spec.partIndex (2026-11-10 design
// D11, §6): the tags' disc and track number when the file carries one, else
// the name's part number, else 0 (path order); the inspect freezes it and
// the execute carries it onto the MediaFile.
func TestAnAudiobookDownloadFreezesEachPartsIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	library, downloads := filepath.Join(dir, "media", "audiobooks"), filepath.Join(dir, "downloads")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)

	qp := nonVideoProfile("audiobook", catalogv1alpha1.ProfileMediaKindAudiobook, "MP3",
		catalogv1alpha1.Tier{Name: "M4B", Qualities: []string{"M4B"}}, catalogv1alpha1.Tier{Name: "MP3", Qualities: []string{"MP3"}})
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "audiobooks", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindAudiobook},
	}
	ab := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "left-hand-of-darkness", Namespace: "media", UID: "left-hand-uid"},
		Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B002V0QK4C", QualityProfileRef: qp.Name, RootFolderRef: rf.Name},
		Status: catalogv1alpha1.AudiobookStatus{
			Metadata: &catalogv1alpha1.AudiobookMetadata{Title: "The Left Hand of Darkness", Authors: []catalogv1alpha1.NamedRef{{Name: "Ursula K. Le Guin"}}},
			Path:     filepath.Join(library, "Ursula K. Le Guin", "The Left Hand of Darkness"),
		},
	}
	c, writes := readOnlyClient(t, qp, rf, ab)
	bus := newBus(t, ctx)
	prober := tagProber{
		"tagged.mp3": {Codec: "mp3", BitrateKbps: 64, DurationMs: 3_600_000, Tags: &commonv1.MediaTags{Album: "The Left Hand of Darkness", DiscNumber: 1, TrackNumber: 3}},
	}
	w := fileimport.NewWorker(c, bus, prober)
	w.Clock = func() time.Time { return now }
	owner := schema.ItemRef{Kind: "Audiobook", Ref: schema.Ref{Namespace: "media", Name: ab.Name, UID: string(ab.UID)}}

	r := newNonVideoRun(t, w, c, owner, commonv1.MediaKindAudiobook, qp, "Ursula K. Le Guin - The Left Hand of Darkness", filepath.Join(downloads, "parts"), now)
	tagged, named, bare := "tagged.mp3", "The Left Hand of Darkness - Part 07.mp3", "The Left Hand of Darkness.mp3"
	files := r.inspected(t, tagged, named, bare)
	for rel, f := range files {
		assert.Empty(t, f.Rejections, rel)
		require.NotNil(t, f.Frozen.Quality, rel)
		assert.Equal(t, "MP3", f.Frozen.Quality.Name, "%s: an audiobook's quality is its extension's", rel)
		assert.Equal(t, filepath.Join(ab.Status.Path, rel), f.Dest, rel)
	}
	assert.EqualValues(t, 1003, files[tagged].Frozen.PartIndex, "disc 1 track 3 from the tags")
	assert.EqualValues(t, 7, files[named].Frozen.PartIndex, "the name's part number")
	assert.Zero(t, files[bare].Frozen.PartIndex, "nothing numbers it: path order")

	r.decide()
	task := r.executeTask()
	require.Len(t, task.Plan.Moves, 3)
	r.deliver()
	dec := r.decide()
	assert.Equal(t, lifecycle.ImportImported, dec.Verdict, "%+v", dec.Summary)
	parts := map[string]int32{}
	for _, m := range dec.Materialise {
		if m.Apply != nil {
			parts[filepath.Base(m.Apply.Path)] = m.Apply.Frozen.PartIndex
			assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: ab.Name}, m.Apply.Ref)
		}
	}
	assert.Equal(t, map[string]int32{tagged: 1003, named: 7, bare: 0}, parts)
	assert.Empty(t, writes(), "the import agent asked for Kubernetes writes")
}
