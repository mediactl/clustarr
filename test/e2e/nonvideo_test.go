//go:build e2e

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

// Scenario 11 (docs/superpowers/plans/2026-09-18-remaining-work.md):
// "Non-video inventory (M6). Artist/Album, Author/Book, Audiobook and
// Comic/Issue through manual import (Download with Manual=true and the
// import-target annotation) -> files stored under the naming presets with
// metadata from the stubs."
//
// Extended for the 2026-11-10 non-video libraries design
// (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md §12, plan
// W6): every leg now asserts the parity fields the design added to the CRDs
// (§4) -- a leaf's status.phase (commonv1.LeafPhase), cutoffReason, hasFile,
// an Album's trackSummary and track fileRefs, an Audiobook's partCount and
// partIndex-ordered fileRefs, an Issue's fileFormat, the containers'
// ContainerRollup counts -- and, under the music and audiobook roots, the
// artist.nfo and album.nfo the import agent writes with their hash in
// status.nfo (§8.1). Metadata comes from test/fixtures/nonvideostub, real
// HTTP, real JSON, never mocked at the gateway boundary -- the same
// MusicBrainz/Open Library/Audnexus/ComicVine fixtures that stub's own unit
// tests drive with the real pkg/metadata/clients.
//
// # Scans where the files must be attributed, manual assignment elsewhere
//
// An album's tracks and an audiobook's parts are attributed by the library
// scan (app/import/worker/rescan's MatchAlbum and MatchAudiobook: the item's
// declared folder, status.path, then fileimport.AttributeTrack by the file's
// tags, its name and its recording id, and fileimport.PartIndexOf by the
// name's part number), so those two legs plant their files under the item's
// resolved status.path and run a Full LibraryScan, which is the flow the
// design's D6 and D11 describe. The manual assignment (the import-target
// annotation on a LibraryScan whose subpath names one file, exactly the
// LibraryScan ui/actions.ManualAssign builds; app/import/worker/rescan/
// manual.go's assignManually) records a file against the item a person
// named and attributes no track and no part, so it stays the path for the
// Book and the Issue, whose items hold one file each.
//
// The planted audio is test/data/mediainfo/audio_mp3_cbr320.mp3 (real MP3,
// untagged, 320 kbps, so the probe freezes MP3-320 -- music-standard's
// "High", above its MP3-192 cutoff -- and the file's NAME attributes the
// track) and the comic test/data/mediainfo/comic_two_pages.cbz (a real
// two-page archive, so the probe's pageCount can be asserted); the book
// stays a few bytes under an .epub name, since a book's quality is its
// extension's and nothing probes one for pages here.
//
// # Why the earlier Album leg's manual assignment went
//
// Until this extension the Artist/Album leg manually assigned one
// "track.mp3" of fake bytes to the album. Since D6 a manually assigned
// album file names no track (assignManually records the item alone), so
// the album would have read hasFile=false with a MediaFile under it -- a
// state the design gives no meaning to -- and the leg could not have
// asserted trackSummary at all. The scan leg above is what the design
// describes; see the leftover on assignManually in the task report.
//
// # Beyond this file
//
// The Cover Art Archive, Wikidata/Wikipedia and Audible routes the stub
// now serves are exercised by its own unit tests; config/e2e/
// metadata-providers.yaml does not yet name a coverart, wikipedia or
// audible MetadataProvider pointed at the stub, and app/catalog/metadata/
// registry.go leaves the Wikipedia summary host at en.wikipedia.org, so
// no leg here asserts an album cover or a biography. The Comic's volume
// search (§4.7's volumeSearch) needs an Indexer and is not driven here.
//
// Build-tagged e2e. Per the standing instruction, this suite is written and
// has never been run against a kind cluster.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/pkg/nfo"
	"github.com/mediactl/clustarr/test/fixtures/nonvideostub"
)

// nonVideoConditionTimeout bounds the wait for a parent's metadata-ready and
// fan-out conditions: one real HTTP round trip to nonvideo-stub plus one
// reconcile, generous margin over indexerReadyTimeout's own reasoning for
// the same shape of wait.
const nonVideoConditionTimeout = 2 * time.Minute

// nonVideoFanOutTimeout bounds the wait for a fan-out of a hundred and more
// children (the Muse browse, two MusicBrainz pages behind the client's
// one-request-a-second limiter, then 175 Album creates).
const nonVideoFanOutTimeout = 4 * time.Minute

// nonVideoImportTimeout bounds the wait for the manual-assign LibraryScan's
// resulting MediaFile to appear.
const nonVideoImportTimeout = 2 * time.Minute

// nonVideoStatusTimeout bounds the wait for an item's status to reflect a
// file that landed, or for an NFO write to land: the loop's item pass after
// the scan applier's MediaFile write, plus one import-nfo round trip, plus
// the MediaFile's probe through the import agent.
const nonVideoStatusTimeout = 3 * time.Minute

// fixtureNonVideoStubService is config/e2e/nonvideo-stub.yaml's Service
// name, matching helpers_test.go's fixtureSeederService/fixtureNNTPStub*
// naming.
const fixtureNonVideoStubService = "nonvideo-stub"

// mediainfoFixtures is test/data/mediainfo relative to this package's
// directory: go test runs a package's tests in that directory, which is how
// test/fixtures/nonvideostub's own test reads ../../data/metadata.
const mediainfoFixtures = "../data/mediainfo"

// fixtureBytes reads one of pkg/mediainfo's real media fixtures.
func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(mediainfoFixtures, name))
	require.NoError(t, err, "read fixture %s (go test runs from test/e2e)", name)
	return b
}

// waitForLive polls obj's live copy until ok holds, failing after timeout
// with the last copy's JSON, since "deadline exceeded" alone says nothing
// about which field never moved.
func waitForLive[T client.Object](ctx context.Context, t *testing.T, timeout time.Duration, desc string, obj T, ok func(T) bool) {
	t.Helper()
	key := client.ObjectKeyFromObject(obj)
	waitFor(t, ctx, timeout, desc, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, obj); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return ok(obj), nil
	}, func() string {
		b, _ := json.Marshal(obj)
		s := string(b)
		if len(s) > 8000 {
			s = s[:8000] + "..."
		}
		return "last observed " + desc + ": " + s
	})
}

// nfoSettled reads the document the import agent wrote for status.nfo and
// reports whether it is the one the manager hashed -- nfo.Hash over the
// document and no image digests, since the e2e cluster fetches no artwork
// (app/remediation/nfo's HashOf) -- and carries each of wants. It is a
// predicate for a wait on purpose: an artist.nfo is rewritten when an
// album's metadata lands, an album.nfo when its tracks are probed and an
// audiobook's when its parts do, so status and file agree only eventually.
func nfoSettled(st *catalogv1alpha1.NFOStatus, folder, file string, wants ...string) bool {
	if st == nil || st.Path != folder || st.Hash == "" || !slices.Contains(st.Files, file) {
		return false
	}
	b, err := os.ReadFile(hostPath(filepath.Join(folder, file)))
	if err != nil || nfo.Hash([][]byte{b}, nil) != st.Hash {
		return false
	}
	doc := string(b)
	for _, w := range wants {
		if !strings.Contains(doc, w) {
			return false
		}
	}
	return true
}

// trackFileName names a planted track the way pkg/release.ParseTrackFileName
// reads its number back (fileimport.AttributeTrack's name rule, D6): "NN -
// Title.mp3" on a one-disc release, "D-NN - Title.mp3" on a longer one, the
// title stripped of what a file name cannot carry.
func trackFileName(media int32, tr catalogv1alpha1.Track) string {
	title := strings.NewReplacer("/", "-", ":", "-", "?", "", "\"", "", "*", "", "<", "", ">", "", "|", "-").Replace(tr.Title)
	if media > 1 {
		return fmt.Sprintf("%d-%02d - %s.mp3", tr.Medium, tr.Number, title)
	}
	return fmt.Sprintf("%02d - %s.mp3", tr.Number, title)
}

// mediaCount is the disc count a track listing spans.
func mediaCount(tracks []catalogv1alpha1.Track) int32 {
	var media int32
	for _, tr := range tracks {
		media = max(media, tr.Medium)
	}
	return media
}

// manualAssignScan creates the exact LibraryScan ui/actions.ManualAssign
// builds (G3-4) -- GenerateName "assign-", the import-target annotation,
// spec.mode Full -- directly against k8sClient, so this file proves the
// WORKER half of manual assignment on its own. The ui half (Options.Actions,
// wired into both ui commands by G3-5) is scenario 14's POST to
// /unmatched/assign in ui_test.go.
func manualAssignScan(ctx context.Context, t *testing.T, rootFolder, subpath string, target importtarget.ImportTarget) *catalogv1alpha1.LibraryScan {
	t.Helper()
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-assign-",
			Namespace:    Namespace,
			Annotations:  map[string]string{importtarget.AnnotationImportTarget: target.String()},
		},
		Spec: catalogv1alpha1.LibraryScanSpec{
			RootFolderRef: rootFolder,
			Subpath:       subpath,
			Mode:          catalogv1alpha1.ScanModeFull,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, scan))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), scan) })
	return scan
}

// waitForMediaFileAt polls for exactly one MediaFile whose spec.path equals
// fullPath, the manual-assign import's own success signal -- more direct
// than waiting on the LibraryScan's own phase, which the never-guess rule
// (CLAUDE.md) can legitimately leave at Completed with the file still
// recorded in status.unmatched if the target was refused.
func waitForMediaFileAt(ctx context.Context, t *testing.T, fullPath string) catalogv1alpha1.MediaFile {
	t.Helper()
	var found catalogv1alpha1.MediaFile
	err := wait.PollUntilContextTimeout(ctx, pollInterval, nonVideoImportTimeout, true, func(ctx context.Context) (bool, error) {
		files := mediaFilesUnder(ctx, t, filepath.Dir(fullPath))
		for _, mf := range files {
			if mf.Spec.Path == fullPath {
				found = mf
				return true, nil
			}
		}
		return false, nil
	})
	require.NoError(t, err, "no MediaFile at %s within %s -- manual-assign import did not land", fullPath, nonVideoImportTimeout)
	return found
}

// TestNonVideoArtistAlbumScanImport is scenario 11's Artist/Album leg: the
// Artist's metadata and rollup, the artist.nfo and album.nfo written before
// any music file exists, then every track of Kid A's selected release
// planted under the Album's resolved folder and imported by a Full scan, so
// the Album reads Imported with trackSummary "N/N" and the Artist counts
// the album's file (design §12: "an Album reading Imported with
// trackSummary", "Artist/Author rollup counts", "the NFO files written
// beside an album and an artist with their hash in status.nfo").
func TestNonVideoArtistAlbumScanImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-music-rf", catalogv1alpha1.RootFolderKindMusic, "music")

	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-artist"), Namespace: Namespace},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID:     nonvideostub.RadioheadMBID, // test/data/metadata/musicbrainz/artist_radiohead_members.json
			QualityProfileRef: "music-standard",           // built-in, seeded by qualityprofile.Bootstrap
			RootFolderRef:     rf.Name,
			Monitored:         new(true),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, artist))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), artist) })

	// The container: metadata fetched, its one album fanned out, its folder
	// resolved, and the ContainerRollup's phase (§4.1).
	waitForLive(ctx, t, nonVideoConditionTimeout, "Artist "+artist.Name+" Ready with its folder and one album", artist, func(a *catalogv1alpha1.Artist) bool {
		return isConditionTrue(a.Status.Conditions, catalogv1alpha1.ArtistConditionMetadataReady) &&
			isConditionTrue(a.Status.Conditions, catalogv1alpha1.ArtistConditionAlbumsSynced) &&
			a.Status.Path != "" && a.Status.Phase == catalogv1alpha1.ContainerPhaseReady && a.Status.AlbumCount == 1
	})
	require.NotNil(t, artist.Status.Metadata)
	require.Equal(t, "Radiohead", artist.Status.Metadata.Name)
	require.Equal(t, "1991", artist.Status.Metadata.Begin, "the life-span begin of the full MusicBrainz recording (§4.2)")
	require.NotEmpty(t, artist.Status.Metadata.Genres, "the full recording's genres")
	require.NotEmpty(t, artist.Status.Metadata.Aliases, "the full recording's aliases")
	require.True(t, slices.ContainsFunc(artist.Status.Metadata.Links, func(l catalogv1alpha1.Link) bool {
		return l.Type == "wikidata" && strings.HasSuffix(l.URL, "/"+nonvideostub.RadioheadWikidataID)
	}), "the wikidata url-rel is a status.metadata.links entry: %+v", artist.Status.Metadata.Links)
	require.True(t, strings.HasPrefix(artist.Status.Path, rf.Spec.Path+"/"), "the artist's folder is under its root: %s", artist.Status.Path)

	var albums catalogv1alpha1.AlbumList
	require.NoError(t, k8sClient.List(ctx, &albums, client.InNamespace(Namespace)))
	var album *catalogv1alpha1.Album
	for i := range albums.Items {
		if albums.Items[i].Spec.ArtistRef == artist.Name {
			album = &albums.Items[i]
			break
		}
	}
	require.NotNil(t, album, "Artist %s fanned out no Album (test/data/metadata/musicbrainz/browse_releasegroups_radiohead.json names one, Kid A)", artist.Name)
	require.Equal(t, nonvideostub.KidAReleaseGroupMBID, album.Spec.ReleaseGroupID, "fanned-out Album must be Kid A, the one release group the fixture browse answers with")

	// The leaf before any file: Wanted (monitored, released in 2000, no
	// file), cutoffReason Pending, the Tracks column "0/N" (§4.3, D4).
	waitForLive(ctx, t, nonVideoConditionTimeout, "Album "+album.Name+" Wanted with its release's tracks", album, func(a *catalogv1alpha1.Album) bool {
		return isConditionTrue(a.Status.Conditions, catalogv1alpha1.AlbumConditionMetadataReady) &&
			a.Status.Path != "" && len(a.Status.Tracks) > 0 &&
			a.Status.Phase == commonv1.LeafPhaseWanted && a.Status.CutoffReason == commonv1.CutoffReasonPending &&
			!a.Status.HasFile && a.Status.TrackSummary == fmt.Sprintf("0/%d", len(a.Status.Tracks))
	})
	require.Equal(t, "Kid A", album.Status.Metadata.Title)
	require.True(t, strings.HasPrefix(album.Status.Path, artist.Status.Path+"/"), "an album's folder is under its artist's: %s under %s", album.Status.Path, artist.Status.Path)
	tracks := album.Status.Tracks
	media := mediaCount(tracks)

	// The rollup counts the wanted album (§4.1: missingChildCount is the
	// monitored, released children without a file).
	waitForLive(ctx, t, nonVideoConditionTimeout, "Artist "+artist.Name+" counts one missing album", artist, func(a *catalogv1alpha1.Artist) bool {
		return a.Status.MissingChildCount == 1 && a.Status.AlbumFileCount == 0 && a.Status.CutoffUnmetChildCount == 0
	})

	// NFO files land as soon as metadata and a folder exist (§8.2: the
	// agent creates the folder), before any music file: artist.nfo with the
	// Kodi elements §8.1 lists and the album link, album.nfo with the
	// artist fallbacks and a track per status.tracks entry, each file the
	// document status.nfo.hash was computed over.
	waitFor(t, ctx, nonVideoStatusTimeout, "artist.nfo written for "+artist.Name, func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Artist
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(artist), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return nfoSettled(live.Status.NFO, live.Status.Path, nfo.FileArtist,
			"<artist>", "<name>Radiohead</name>",
			"<musicBrainzArtistID>"+nonvideostub.RadioheadMBID+"</musicBrainzArtistID>",
			"<sortname>Radiohead</sortname>", "<type>Group</type>", "<formed>1991</formed>", "<genre>",
			"<album>", "<title>Kid A</title>",
			"<musicbrainzreleasegroupid>"+nonvideostub.KidAReleaseGroupMBID+"</musicbrainzreleasegroupid>"), nil
	}, func() string {
		var live catalogv1alpha1.Artist
		_ = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(artist), &live)
		return fmt.Sprintf("status.nfo=%+v path=%s", live.Status.NFO, live.Status.Path)
	})
	waitFor(t, ctx, nonVideoStatusTimeout, "album.nfo written for "+album.Name, func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Album
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(album), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return nfoSettled(live.Status.NFO, live.Status.Path, nfo.FileAlbum,
			"<album>", "<title>Kid A</title>",
			"<musicbrainzreleasegroupid>"+nonvideostub.KidAReleaseGroupMBID+"</musicbrainzreleasegroupid>",
			"<artistdesc>Radiohead</artistdesc>", "<albumartist>Radiohead</albumartist>",
			"<musicBrainzArtistID>"+nonvideostub.RadioheadMBID+"</musicBrainzArtistID>",
			"<track>", "<title>"+tracks[0].Title+"</title>", "<musicBrainzTrackID>"+tracks[0].RecordingID+"</musicBrainzTrackID>"), nil
	}, func() string {
		var live catalogv1alpha1.Album
		_ = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(album), &live)
		return fmt.Sprintf("status.nfo=%+v path=%s", live.Status.NFO, live.Status.Path)
	})

	// Every track of the selected release, named for its number, under the
	// album's declared folder, then a Full scan: MatchAlbum attributes each
	// file to the album by folder and AttributeTrack to its track by name
	// (the fixture is untagged), freezing MP3-320 from the probe.
	audio := fixtureBytes(t, "audio_mp3_cbr320.mp3")
	wantTracks := map[string]bool{}
	for _, tr := range tracks {
		wantTracks[tr.RecordingID] = true
		plantBytes(t, hostPath(filepath.Join(album.Status.Path, trackFileName(media, tr))), audio)
	}
	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	var files []catalogv1alpha1.MediaFile
	waitFor(t, ctx, nonVideoImportTimeout, fmt.Sprintf("%d MediaFiles under %s", len(tracks), album.Status.Path), func(ctx context.Context) (bool, error) {
		files = mediaFilesUnder(ctx, t, album.Status.Path)
		return len(files) == len(tracks), nil
	}, func() string { return fmt.Sprintf("%d MediaFiles under %s", len(files), album.Status.Path) })
	gotTracks := map[string]bool{}
	for _, mf := range files {
		require.Equal(t, commonv1.MediaKindAlbum, mf.Spec.MediaRef.Kind)
		require.Equal(t, album.Name, mf.Spec.MediaRef.Name)
		require.True(t, wantTracks[mf.Spec.MediaRef.Track], "%s is attributed to recording %q, which the selected release does not list", mf.Spec.Path, mf.Spec.MediaRef.Track)
		require.False(t, gotTracks[mf.Spec.MediaRef.Track], "two files attributed to recording %q", mf.Spec.MediaRef.Track)
		gotTracks[mf.Spec.MediaRef.Track] = true
		require.NotNil(t, mf.Spec.Quality, "a probed MP3's quality is frozen at import")
		require.Equal(t, "High", mf.Spec.Quality.Name, "320 kbps MP3 is Lidarr's High tier (release.AudioFileQuality)")
	}

	// The leaf with every track on disk at the cutoff: Imported, its
	// counts, every track's fileRef (§4.3, D4, D6).
	waitForLive(ctx, t, nonVideoStatusTimeout, "Album "+album.Name+" Imported", album, func(a *catalogv1alpha1.Album) bool {
		if !a.Status.HasFile || a.Status.Phase != commonv1.LeafPhaseImported || !a.Status.CutoffMet ||
			a.Status.CutoffReason != commonv1.CutoffReasonCutoffMet ||
			int(a.Status.TrackFileCount) != len(tracks) || a.Status.TrackSummary != fmt.Sprintf("%d/%d", len(tracks), len(tracks)) {
			return false
		}
		for _, tr := range a.Status.Tracks {
			if tr.FileRef == nil || *tr.FileRef == "" {
				return false
			}
		}
		return true
	})
	require.NotNil(t, album.Status.FileQuality)
	require.Equal(t, "High", album.Status.FileQuality.Name, "the album's quality is its lowest track's")

	// The container's rollup moves with its child (§4.1, §5.2).
	waitForLive(ctx, t, nonVideoStatusTimeout, "Artist "+artist.Name+" counts the imported album", artist, func(a *catalogv1alpha1.Artist) bool {
		return a.Status.AlbumFileCount == 1 && a.Status.MissingChildCount == 0 && a.Status.CutoffUnmetChildCount == 0 &&
			a.Status.Phase == catalogv1alpha1.ContainerPhaseReady
	})

	// The probe (§6, D9): the remediation loop asks the import agent to
	// probe every MediaFile, audio included, and the record lands in
	// status.mediaInfo as pkg/mediainfo/native reads it.
	probed := files[0]
	waitForLive(ctx, t, nonVideoStatusTimeout, "MediaFile "+probed.Name+" probed", &probed, func(mf *catalogv1alpha1.MediaFile) bool {
		mi := mf.Status.MediaInfo
		return mi != nil && mi.Container == "mp3" && len(mi.Audio) == 1 && mi.Audio[0].Codec == "mp3" &&
			mi.Audio[0].BitrateKbps == 320 && mi.Audio[0].SampleRateHz == 44100
	})
	require.Empty(t, probed.Status.MediaInfo.VideoCodec, "an audio file probes with no video")
}

// TestNonVideoArtistBrowsePagesEveryReleaseGroup: an artist with more
// release groups than one MusicBrainz browse page holds (Muse: 175, two
// pages of 100 recorded as browse_releasegroups_muse_p1/p2.json) fans out
// every one of them -- pkg/metadata/clients/musicbrainz's Albums pages by
// offset and the stub serves each page by its offset -- and the Artist's
// metadata carries the full recording's aliases, links and life-span (§4.2,
// §5.4). The artist is unmonitored so its 175 albums are not searched: the
// leg is about the browse and the rollup, and the Album lookups themselves
// 404 (the stub holds Kid A's release group alone), which leaves each
// Album Pending without failing anything here.
func TestNonVideoArtistBrowsePagesEveryReleaseGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-muse-rf", catalogv1alpha1.RootFolderKindMusic, "music")

	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-muse"), Namespace: Namespace},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID:     nonvideostub.MuseMBID, // test/data/metadata/musicbrainz/artist_muse_annotation_aliases.json
			QualityProfileRef: "music-standard",
			RootFolderRef:     rf.Name,
			Monitored:         new(false),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, artist))
	// The Albums are controller-owned by the Artist (artist/reconciler.go's
	// SetControllerReference), so deleting it collects all 175.
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), artist) })

	waitForLive(ctx, t, nonVideoFanOutTimeout, "Artist "+artist.Name+" fanned out every release group", artist, func(a *catalogv1alpha1.Artist) bool {
		return isConditionTrue(a.Status.Conditions, catalogv1alpha1.ArtistConditionMetadataReady) &&
			isConditionTrue(a.Status.Conditions, catalogv1alpha1.ArtistConditionAlbumsSynced) &&
			a.Status.Path != "" && a.Status.AlbumCount == nonvideostub.MuseReleaseGroupCount
	})
	require.Equal(t, catalogv1alpha1.ContainerPhaseUnmonitored, artist.Status.Phase, "an unmonitored container reads Unmonitored (§4.1)")
	require.NotNil(t, artist.Status.Metadata)
	require.Equal(t, "Muse", artist.Status.Metadata.Name)
	require.Equal(t, "1994", artist.Status.Metadata.Begin)
	require.NotEmpty(t, artist.Status.Metadata.Aliases, "the recording's aliases (§4.2)")
	require.True(t, slices.ContainsFunc(artist.Status.Metadata.Links, func(l catalogv1alpha1.Link) bool { return l.Type == "discogs" }),
		"the recording's discogs url-rel is a status.metadata.links entry: %+v", artist.Status.Metadata.Links)

	var albums catalogv1alpha1.AlbumList
	require.NoError(t, k8sClient.List(ctx, &albums, client.InNamespace(Namespace)))
	seen := map[string]bool{}
	for i := range albums.Items {
		if albums.Items[i].Spec.ArtistRef != artist.Name {
			continue
		}
		require.False(t, seen[albums.Items[i].Spec.ReleaseGroupID], "release group %s fanned out twice", albums.Items[i].Spec.ReleaseGroupID)
		seen[albums.Items[i].Spec.ReleaseGroupID] = true
	}
	require.Len(t, seen, nonvideostub.MuseReleaseGroupCount, "one Album per release group of both browse pages, none twice")

	// artist.nfo links every album (§8.1's <album> list), whatever the
	// albums' own lookups did.
	waitFor(t, ctx, nonVideoStatusTimeout, "artist.nfo written for "+artist.Name, func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Artist
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(artist), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		if !nfoSettled(live.Status.NFO, live.Status.Path, nfo.FileArtist, "<name>Muse</name>", "<formed>1994</formed>", "<album>") {
			return false, nil
		}
		b, err := os.ReadFile(hostPath(filepath.Join(live.Status.Path, nfo.FileArtist)))
		return err == nil && strings.Count(string(b), "<musicbrainzreleasegroupid>") == nonvideostub.MuseReleaseGroupCount, nil
	})
}

// TestNonVideoAuthorBookManualImport is scenario 11's Author/Book leg: the
// Author's metadata, folder and rollup, the Book's phase before and after a
// manually assigned .epub, and no NFO under a book root (§8.1 writes them
// under music and audiobook roots alone).
func TestNonVideoAuthorBookManualImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-book-rf", catalogv1alpha1.RootFolderKindBook, "books")

	author := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-author"), Namespace: Namespace},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID:     "OL21594A", // test/data/metadata/openlibrary/author_OL21594A.json
			QualityProfileRef: "ebook",    // built-in
			RootFolderRef:     rf.Name,
			Monitored:         new(true),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, author))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), author) })

	waitForLive(ctx, t, nonVideoConditionTimeout, "Author "+author.Name+" Ready with its folder and one book", author, func(a *catalogv1alpha1.Author) bool {
		return isConditionTrue(a.Status.Conditions, catalogv1alpha1.AuthorConditionMetadataReady) &&
			isConditionTrue(a.Status.Conditions, catalogv1alpha1.AuthorConditionBooksSynced) &&
			a.Status.Path != "" && a.Status.Phase == catalogv1alpha1.ContainerPhaseReady && a.Status.BookCount == 1
	})
	require.NotNil(t, author.Status.Metadata)
	require.Equal(t, "Jane Austen", author.Status.Metadata.Name)

	var books catalogv1alpha1.BookList
	require.NoError(t, k8sClient.List(ctx, &books, client.InNamespace(Namespace)))
	var book *catalogv1alpha1.Book
	for i := range books.Items {
		if books.Items[i].Spec.AuthorRef != nil && *books.Items[i].Spec.AuthorRef == author.Name {
			book = &books.Items[i]
			break
		}
	}
	require.NotNil(t, book, "Author %s fanned out no Book (test/data/metadata/openlibrary/search_works_author_OL21594A.json)", author.Name)

	// Pride and Prejudice (1813): released, monitored, no file -- Wanted
	// (§4.5, D4).
	waitForLive(ctx, t, nonVideoConditionTimeout, "Book "+book.Name+" Wanted", book, func(b *catalogv1alpha1.Book) bool {
		return isConditionTrue(b.Status.Conditions, catalogv1alpha1.BookConditionMetadataReady) &&
			b.Status.Phase == commonv1.LeafPhaseWanted && b.Status.CutoffReason == commonv1.CutoffReasonPending && !b.Status.HasFile
	})
	require.Equal(t, "Pride and Prejudice", book.Status.Metadata.Title)
	waitForLive(ctx, t, nonVideoConditionTimeout, "Author "+author.Name+" counts one missing book", author, func(a *catalogv1alpha1.Author) bool {
		return a.Status.MissingChildCount == 1 && a.Status.BookFileCount == 0
	})

	relSubpath := filepath.Join(author.Name, "book.epub")
	fullPath := filepath.Join(rf.Spec.Path, relSubpath)
	plantBytes(t, hostPath(fullPath), []byte("epub-fixture-e2e11-book"))

	manualAssignScan(ctx, t, rf.Name, relSubpath, importtarget.ImportTarget{Kind: commonv1.MediaKindBook, Name: book.Name})

	mf := waitForMediaFileAt(ctx, t, fullPath)
	require.Equal(t, commonv1.MediaKindBook, mf.Spec.MediaRef.Kind)
	require.Equal(t, book.Name, mf.Spec.MediaRef.Name)
	require.NotNil(t, mf.Spec.Quality, "a book's quality is frozen from its extension")
	require.Equal(t, "EPUB", mf.Spec.Quality.Name)

	// EPUB sits above ebook's MOBI cutoff: Imported, the file's format and
	// ref on the leaf (§4.5).
	waitForLive(ctx, t, nonVideoStatusTimeout, "Book "+book.Name+" Imported", book, func(b *catalogv1alpha1.Book) bool {
		return b.Status.HasFile && b.Status.FileRef != nil && *b.Status.FileRef == mf.Name &&
			b.Status.FileFormat == "EPUB" && b.Status.Phase == commonv1.LeafPhaseImported &&
			b.Status.CutoffReason == commonv1.CutoffReasonCutoffMet && b.Status.CutoffMet
	})
	require.NotNil(t, book.Status.FileQuality)
	require.Equal(t, "EPUB", book.Status.FileQuality.Name)

	waitForLive(ctx, t, nonVideoStatusTimeout, "Author "+author.Name+" counts the imported book", author, func(a *catalogv1alpha1.Author) bool {
		return a.Status.BookFileCount == 1 && a.Status.MissingChildCount == 0 && a.Status.Phase == catalogv1alpha1.ContainerPhaseReady
	})
	require.Nil(t, author.Status.NFO, "a book root writes no NFO (§8.1: music and audiobook roots alone; a Book has no status.nfo at all)")
}

// TestNonVideoAudiobookScanImportsParts is scenario 11's Audiobook leg.
// Audiobook has no child kind (the leaf itself, the same shape as Movie),
// and since D11 it holds several parts: two "Part N" files planted under
// the Audiobook's resolved folder and imported by a Full scan read as
// fileRefs in partIndex order, partCount 2, Imported; the audiobook root's
// album.nfo names the audiobook's author as its artist and, once the parts
// are on disk, one track per part titled by its chapter (the Audnexus
// recording has two chapters, so parts and chapters align; §8.1).
func TestNonVideoAudiobookScanImportsParts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-audiobook-rf", catalogv1alpha1.RootFolderKindAudiobook, "audiobooks")

	audiobook := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-audiobook"), Namespace: Namespace},
		Spec: catalogv1alpha1.AudiobookSpec{
			ASIN:              "B0036I54I6", // test/data/metadata/audnexus/book_B0036I54I6.json
			QualityProfileRef: "audiobook",  // built-in
			RootFolderRef:     rf.Name,
			Monitored:         new(true),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, audiobook))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), audiobook) })

	// The Hobbit (2012): released, monitored, no file -- Wanted (§4.6).
	waitForLive(ctx, t, nonVideoConditionTimeout, "Audiobook "+audiobook.Name+" Wanted with its folder", audiobook, func(a *catalogv1alpha1.Audiobook) bool {
		return isConditionTrue(a.Status.Conditions, catalogv1alpha1.AudiobookConditionMetadataReady) &&
			a.Status.Path != "" && a.Status.Phase == commonv1.LeafPhaseWanted &&
			a.Status.CutoffReason == commonv1.CutoffReasonPending && !a.Status.HasFile && a.Status.PartCount == 0
	})
	require.NotNil(t, audiobook.Status.Metadata)
	require.Equal(t, "The Hobbit", audiobook.Status.Metadata.Title)
	require.NotEmpty(t, audiobook.Status.Metadata.Authors)
	require.Equal(t, "J.R.R. Tolkien", audiobook.Status.Metadata.Authors[0].Name)
	require.Len(t, audiobook.Status.Metadata.Chapters, 2, "the recorded chapters (chapters_B0036I54I6.json), which the parts below align with")
	require.True(t, strings.HasPrefix(audiobook.Status.Path, rf.Spec.Path+"/"), "the audiobook's folder is under its root: %s", audiobook.Status.Path)

	// album.nfo before any part: the audiobook as an album of its author
	// (§8.1 "Audiobooks").
	waitFor(t, ctx, nonVideoStatusTimeout, "album.nfo written for "+audiobook.Name, func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Audiobook
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(audiobook), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return nfoSettled(live.Status.NFO, live.Status.Path, nfo.FileAlbum,
			"<album>", "<title>The Hobbit</title>", "<type>Audiobook</type>",
			"<artistdesc>J.R.R. Tolkien</artistdesc>", "<albumartist>J.R.R. Tolkien</albumartist>"), nil
	}, func() string {
		var live catalogv1alpha1.Audiobook
		_ = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(audiobook), &live)
		return fmt.Sprintf("status.nfo=%+v path=%s", live.Status.NFO, live.Status.Path)
	})

	// Two parts, named with their part number (release.PartOf reads "Part
	// 2"; fileimport.PartIndexOf freezes it as spec.partIndex since the
	// fixture carries no track tag), under the declared folder, then a Full
	// scan: MatchAudiobook attributes both by folder.
	audio := fixtureBytes(t, "audio_mp3_cbr320.mp3")
	part1 := filepath.Join(audiobook.Status.Path, "The Hobbit - Part 1.mp3")
	part2 := filepath.Join(audiobook.Status.Path, "The Hobbit - Part 2.mp3")
	// Planted in reverse so an ordering by creation or by walk order alone
	// would differ from the one partIndex gives.
	plantBytes(t, hostPath(part2), audio)
	plantBytes(t, hostPath(part1), audio)
	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	var files []catalogv1alpha1.MediaFile
	waitFor(t, ctx, nonVideoImportTimeout, "2 MediaFiles under "+audiobook.Status.Path, func(ctx context.Context) (bool, error) {
		files = mediaFilesUnder(ctx, t, audiobook.Status.Path)
		return len(files) == 2, nil
	}, func() string { return fmt.Sprintf("%d MediaFiles under %s", len(files), audiobook.Status.Path) })
	byPath := map[string]catalogv1alpha1.MediaFile{}
	for _, mf := range files {
		require.Equal(t, commonv1.MediaKindAudiobook, mf.Spec.MediaRef.Kind)
		require.Equal(t, audiobook.Name, mf.Spec.MediaRef.Name)
		require.NotNil(t, mf.Spec.Quality)
		require.Equal(t, "MP3", mf.Spec.Quality.Name, "an audiobook's quality is its format (frozenQualityNames)")
		byPath[mf.Spec.Path] = mf
	}
	require.Contains(t, byPath, part1)
	require.Contains(t, byPath, part2)
	require.EqualValues(t, 1, byPath[part1].Spec.PartIndex, "spec.partIndex is the name's part number (D11)")
	require.EqualValues(t, 2, byPath[part2].Spec.PartIndex)

	// The leaf: both parts, in play order, at the MP3 cutoff -- Imported
	// (§4.6, D11).
	waitForLive(ctx, t, nonVideoStatusTimeout, "Audiobook "+audiobook.Name+" Imported with two ordered parts", audiobook, func(a *catalogv1alpha1.Audiobook) bool {
		return a.Status.HasFile && a.Status.PartCount == 2 &&
			slices.Equal(a.Status.FileRefs, []string{byPath[part1].Name, byPath[part2].Name}) &&
			a.Status.Phase == commonv1.LeafPhaseImported && a.Status.CutoffReason == commonv1.CutoffReasonCutoffMet && a.Status.CutoffMet
	})
	require.NotNil(t, audiobook.Status.FileQuality)
	require.Equal(t, "MP3", audiobook.Status.FileQuality.Name)

	// album.nfo again, now with a track per part titled by its chapter,
	// rewritten under a new hash (§8.1: "<track> per part with the chapter
	// title when parts and chapters align").
	chapters := audiobook.Status.Metadata.Chapters
	waitFor(t, ctx, nonVideoStatusTimeout, "album.nfo rewritten with the parts for "+audiobook.Name, func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Audiobook
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(audiobook), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return nfoSettled(live.Status.NFO, live.Status.Path, nfo.FileAlbum,
			"<type>Audiobook</type>", "<track>", "<position>1</position>", "<position>2</position>",
			"<title>"+chapters[0].Title+"</title>", "<title>"+chapters[1].Title+"</title>"), nil
	}, func() string {
		var live catalogv1alpha1.Audiobook
		_ = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(audiobook), &live)
		return fmt.Sprintf("status.nfo=%+v fileRefs=%v", live.Status.NFO, live.Status.FileRefs)
	})
}

// TestNonVideoComicIssueManualImport is scenario 11's Comic/Issue leg. It
// uses the plain "issue/<name>" target form (importtarget.ParseImportTarget's
// two-segment grammar), not the "comic/<c>/<key>" three-segment alternative
// the design note also allows: both resolve to the identical
// FileRef{Kind: Issue, Name: ...} (annotation.go's FileRef, the Key branch
// for MediaKindComic), and the two-segment form needs only the Issue's own
// object name -- which this test already has from listing Issues -- not a
// second lookup of the issue's printed number. The planted file is a real
// two-page CBZ, so the probe's pageCount (§4.1, D9) is asserted on the
// MediaFile.
func TestNonVideoComicIssueManualImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-comic-rf", catalogv1alpha1.RootFolderKindComic, "comics")

	comic := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-comic"), Namespace: Namespace},
		Spec: catalogv1alpha1.ComicSpec{
			Source:            catalogv1alpha1.ComicSourceComicVine,
			SourceID:          "4050-18257", // test/data/metadata/comicvine/volume_18257.json
			QualityProfileRef: "comic",      // built-in
			RootFolderRef:     rf.Name,
			Monitored:         new(true),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, comic))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), comic) })

	waitForLive(ctx, t, nonVideoConditionTimeout, "Comic "+comic.Name+" Ready with its folder and issues", comic, func(c *catalogv1alpha1.Comic) bool {
		return isConditionTrue(c.Status.Conditions, catalogv1alpha1.ComicConditionMetadataReady) &&
			isConditionTrue(c.Status.Conditions, catalogv1alpha1.ComicConditionIssuesSynced) &&
			c.Status.Path != "" && c.Status.Phase == catalogv1alpha1.ContainerPhaseReady
	})
	require.NotNil(t, comic.Status.Metadata)
	require.Equal(t, "Batman", comic.Status.Metadata.Title)

	var issues catalogv1alpha1.IssueList
	require.NoError(t, k8sClient.List(ctx, &issues, client.InNamespace(Namespace)))
	var issue *catalogv1alpha1.Issue
	for i := range issues.Items {
		if issues.Items[i].Spec.ComicRef == comic.Name {
			issue = &issues.Items[i]
			break
		}
	}
	require.NotNil(t, issue, "Comic %s fanned out no Issue (test/data/metadata/comicvine/issues_volume_18257.json)", comic.Name)

	// Batman #1 (1940), monitored by addOptions.monitor's default (all):
	// Wanted (§4.7, D4), and its comic counts it missing.
	waitForLive(ctx, t, nonVideoConditionTimeout, "Issue "+issue.Name+" Wanted", issue, func(is *catalogv1alpha1.Issue) bool {
		return is.Status.Phase == commonv1.LeafPhaseWanted && is.Status.CutoffReason == commonv1.CutoffReasonPending && !is.Status.HasFile
	})
	waitForLive(ctx, t, nonVideoConditionTimeout, "Comic "+comic.Name+" counts one missing issue", comic, func(c *catalogv1alpha1.Comic) bool {
		return c.Status.MissingChildCount == 1 && c.Status.IssueFileCount == 0
	})

	relSubpath := filepath.Join(comic.Name, fmt.Sprintf("issue-%s.cbz", issue.Spec.Number))
	fullPath := filepath.Join(rf.Spec.Path, relSubpath)
	plantBytes(t, hostPath(fullPath), fixtureBytes(t, "comic_two_pages.cbz"))

	manualAssignScan(ctx, t, rf.Name, relSubpath, importtarget.ImportTarget{Kind: commonv1.MediaKindIssue, Name: issue.Name})

	mf := waitForMediaFileAt(ctx, t, fullPath)
	require.Equal(t, commonv1.MediaKindIssue, mf.Spec.MediaRef.Kind)
	require.Equal(t, issue.Name, mf.Spec.MediaRef.Name)
	require.NotNil(t, mf.Spec.Quality)
	require.Equal(t, "CBZ", mf.Spec.Quality.Name)

	// CBZ is comic's cutoff: Imported, the format and ref on the leaf
	// (§4.7).
	waitForLive(ctx, t, nonVideoStatusTimeout, "Issue "+issue.Name+" Imported", issue, func(is *catalogv1alpha1.Issue) bool {
		return is.Status.HasFile && is.Status.FileRef != nil && *is.Status.FileRef == mf.Name &&
			is.Status.FileFormat == "CBZ" && is.Status.Phase == commonv1.LeafPhaseImported &&
			is.Status.CutoffReason == commonv1.CutoffReasonCutoffMet
	})
	waitForLive(ctx, t, nonVideoStatusTimeout, "Comic "+comic.Name+" counts the imported issue", comic, func(c *catalogv1alpha1.Comic) bool {
		return c.Status.IssueFileCount == 1 && c.Status.MissingChildCount == 0 && c.Status.Phase == catalogv1alpha1.ContainerPhaseReady
	})

	// The probe counts the archive's pages (D9: pkg/mediainfo/pagecount in
	// the import agent's probe worker).
	waitForLive(ctx, t, nonVideoStatusTimeout, "MediaFile "+mf.Name+" probed for its pages", &mf, func(m *catalogv1alpha1.MediaFile) bool {
		return m.Status.MediaInfo != nil && m.Status.MediaInfo.PageCount == 2
	})
}
