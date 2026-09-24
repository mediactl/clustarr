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

package usenet

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/javi11/rapidyenc"
	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// fileSpec is one file in a synthetic NZB: its name and its decoded parts.
type fileSpec struct {
	name  string
	parts [][]byte
}

// buildNZB writes an NZB for the given files and registers every article on
// srv. It returns the .nzb body.
//
// The XML is generated rather than kept as a fixture so a test can say what it
// means -- "five segments, one of which the primary refuses" -- instead of
// hiding it in testdata.
func buildNZB(t *testing.T, srv *stubServer, title string, files []fileSpec) []byte {
	t.Helper()
	return buildNZBWithPrefix(t, srv, title, "", files)
}

// buildNZBWithPrefix is buildNZB with every message-id prefixed, so two NZBs
// registered on one stubServer do not overwrite each other's articles.
func buildNZBWithPrefix(t *testing.T, srv *stubServer, title, idPrefix string, files []fileSpec) []byte {
	t.Helper()

	type seg struct {
		Bytes  int    `xml:"bytes,attr"`
		Number int    `xml:"number,attr"`
		ID     string `xml:",chardata"`
	}
	type file struct {
		Poster   string   `xml:"poster,attr"`
		Date     int64    `xml:"date,attr"`
		Subject  string   `xml:"subject,attr"`
		Groups   []string `xml:"groups>group"`
		Segments []seg    `xml:"segments>segment"`
	}
	type meta struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	}
	type nzbDoc struct {
		XMLName xml.Name `xml:"nzb"`
		NS      string   `xml:"xmlns,attr"`
		Head    []meta   `xml:"head>meta"`
		Files   []file   `xml:"file"`
	}

	doc := nzbDoc{NS: "http://www.newzbin.com/DTD/2003/nzb"}
	if title != "" {
		doc.Head = append(doc.Head, meta{Type: "title", Value: title})
	}

	for fi, f := range files {
		var size int64
		for _, p := range f.parts {
			size += int64(len(p))
		}
		nf := file{
			Poster:  "poster@clustarr.test",
			Date:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix(),
			Subject: fmt.Sprintf("[%d/%d] - \"%s\" yEnc (1/%d)", fi+1, len(files), f.name, len(f.parts)),
			Groups:  []string{"alt.binaries.clustarr"},
		}
		var offset int64
		for pi, payload := range f.parts {
			id := fmt.Sprintf("%sf%d-p%d@clustarr.test", idPrefix, fi, pi)
			art := stubArticle{data: payload, meta: rapidyenc.Meta{
				FileName: f.name, FileSize: size, PartNumber: int64(pi + 1),
				TotalParts: int64(len(f.parts)), Offset: offset, PartSize: int64(len(payload)),
			}}
			if srv != nil {
				srv.addArticle(id, f.name, size, offset, pi+1, len(f.parts), payload)
			}
			// A real NZB records the ENCODED size of the article, which yEnc
			// makes 2-3% larger than the payload. Recording the decoded size
			// here instead would let a client that counts decoded bytes
			// against those totals report 100% and still be wrong.
			encoded, encErr := encodePart(art)
			require.NoError(t, encErr)
			nf.Segments = append(nf.Segments, seg{Bytes: len(encoded), Number: pi + 1, ID: id})
			offset += int64(len(payload))
		}
		doc.Files = append(doc.Files, nf)
	}

	body, err := xml.Marshal(doc)
	require.NoError(t, err)
	return append([]byte(xml.Header), body...)
}

func newTestClient(t *testing.T, cfg Config) (*Client, string, string) {
	t.Helper()
	root := t.TempDir()
	cfg.ScratchDir = filepath.Join(root, "scratch")
	cfg.DataDir = filepath.Join(root, "data")
	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c.(*Client), cfg.ScratchDir, cfg.DataDir
}

func waitForTerminal(t *testing.T, c download.Client, id string) download.Item {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		it, err := c.Get(context.Background(), id)
		require.NoError(t, err)
		if it.Status == download.StatusCompleted || it.Status == download.StatusFailed {
			return it
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("download %s did not reach a terminal state", id)
	return download.Item{}
}

// PublishDir is where the owner's cluster wants finished content
// (/data/usenet/complete), separate from the data root the removals and the
// free-space check are contained to.
func TestClientPublishesUnderPublishDir(t *testing.T) {
	srv := newStubServer(t)
	parts := [][]byte{partPayload(1, 900), partPayload(2, 512)}
	nzb := buildNZB(t, srv, "Some.Movie.2026.1080p", []fileSpec{{name: "movie.mkv", parts: parts}})

	root := t.TempDir()
	cfg := Config{
		Providers:  []Provider{srv.provider("solo", 4, 1)},
		ScratchDir: filepath.Join(root, "usenet", "incomplete"),
		DataDir:    root,
		PublishDir: filepath.Join(root, "usenet", "complete"),
	}
	cl, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })
	c := cl.(*Client)

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Some.Movie.2026.1080p", Payload: nzb, Category: "movies"})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, filepath.Join(root, "usenet", "complete", "movies", "Some.Movie.2026.1080p"), it.OutputPath)
	_, err = os.Stat(filepath.Join(it.OutputPath, "movie.mkv"))
	require.NoError(t, err)

	// Removal with data is contained to the publish dir and takes it away.
	require.NoError(t, c.Remove(context.Background(), id, true))
	_, err = os.Stat(it.OutputPath)
	require.True(t, os.IsNotExist(err), "the published content is removed with the transfer")
}

func TestClientDownloadsAnNZBAndPublishesIt(t *testing.T) {
	srv := newStubServer(t)
	parts := [][]byte{partPayload(1, 900), partPayload(2, 900), partPayload(3, 512)}
	nzb := buildNZB(t, srv, "Some.Movie.2026.1080p", []fileSpec{{name: "movie.mkv", parts: parts}})

	c, _, dataDir := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 4, 1)}})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Some.Movie.2026.1080p", Payload: nzb, Category: "movies"})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, downloadv1alpha1.DownloadStageDone, it.Stage)
	require.True(t, it.CanMoveFiles, "a published usenet transfer may be moved immediately")
	require.False(t, it.CanBeRemoved, "not until the import says so")
	require.Equal(t, int32(100), it.ProgressPercent)
	require.NotNil(t, it.Health)
	require.Equal(t, int32(100), it.Health.HealthPercent)
	require.Equal(t, int32(0), it.Health.FailedArticles)
	require.Equal(t, int32(3), it.Health.TotalArticles)

	want := bytes.Join(parts, nil)
	got, err := os.ReadFile(filepath.Join(dataDir, "movies", "Some.Movie.2026.1080p", "movie.mkv"))
	require.NoError(t, err)
	require.Equal(t, want, got, "the assembled file must be byte-identical to what was posted")

	require.Len(t, it.Files, 1)
	require.Equal(t, "movie.mkv", it.Files[0].Path)
	require.Equal(t, int64(len(want)), it.Files[0].SizeBytes)
	require.Equal(t, it.OutputPath, it.ContentRoot)

	// The manifest and the stored NZB stay behind in the scratch area; the
	// content is what moved.
	require.NoFileExists(t, filepath.Join(dataDir, "movies", "Some.Movie.2026.1080p", manifestName))
}

func TestClientAddIsIdempotentOnTheReturnedID(t *testing.T) {
	// The re-attach path depends on this: a controller that did not observe
	// the first Add must not be able to start a second copy.
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Dup", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(7, 600)}}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})

	first, err := c.Add(context.Background(), download.AddRequest{Name: "Dup", Payload: nzb})
	require.NoError(t, err)
	waitForTerminal(t, c, first)

	second, err := c.Add(context.Background(), download.AddRequest{Name: "Dup", Payload: nzb})
	require.NoError(t, err)
	require.Equal(t, first, second, "the same payload must return the same id")

	items, err := c.List(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1, "a second Add must not create a second transfer")
	require.Equal(t, download.StatusCompleted, items[0].Status,
		"a second Add must not restart a finished transfer")
	require.Equal(t, 1, srv.servedCount("f0-p0@clustarr.test"),
		"the article must not be fetched twice")
}

func TestClientReattachesAfterARestart(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Restart", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(8, 700)}}})

	root := t.TempDir()
	cfg := Config{
		Providers:  []Provider{srv.provider("solo", 2, 1)},
		ScratchDir: filepath.Join(root, "scratch"),
		DataDir:    filepath.Join(root, "data"),
	}

	first, err := New(cfg)
	require.NoError(t, err)
	id, err := first.Add(context.Background(), download.AddRequest{Name: "Restart", Payload: nzb})
	require.NoError(t, err)
	waitForTerminal(t, first, id)
	require.NoError(t, first.Close())

	second, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	it, err := second.Get(context.Background(), id)
	require.NoError(t, err, "a restarted engine must still know the id it issued")
	require.Equal(t, download.StatusCompleted, it.Status)

	again, err := second.Add(context.Background(), download.AddRequest{Name: "Restart", Payload: nzb})
	require.NoError(t, err)
	require.Equal(t, id, again)
	require.Equal(t, 1, srv.servedCount("f0-p0@clustarr.test"),
		"re-attach must not re-download a finished transfer")

	// A re-attached transfer that was already finished has no goroutine
	// behind it, so Remove must not sit out the grace period waiting for one
	// to end.
	start := time.Now()
	require.NoError(t, second.Remove(context.Background(), id, false))
	require.Less(t, time.Since(start), 5*time.Second,
		"Remove waited for a goroutine that was never started")
}

func TestClientFailsADownloadWithTooManyMissingArticles(t *testing.T) {
	srv := newStubServer(t)
	parts := [][]byte{partPayload(1, 400), partPayload(2, 400), partPayload(3, 400), partPayload(4, 400)}
	nzb := buildNZB(t, srv, "Swiss.Cheese", []fileSpec{{name: "movie.mkv", parts: parts}})
	srv.refuse["f0-p1@clustarr.test"] = 430
	srv.refuse["f0-p2@clustarr.test"] = 430

	c, _, _ := newTestClient(t, Config{
		Providers:    []Provider{srv.provider("solo", 2, 1)},
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Swiss.Cheese", Payload: nzb})
	require.NoError(t, err)

	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status)
	require.Equal(t, downloadv1alpha1.DownloadFailureMissingArticles, it.FailureReason)
	require.False(t, it.HealthPaused, "healthAction delete fails the job; it does not pause it")
	require.NotNil(t, it.Health)
	require.Equal(t, int32(2), it.Health.FailedArticles)
	require.Equal(t, int32(4), it.Health.TotalArticles)
	require.Equal(t, int32(50), it.Health.HealthPercent)
	require.Equal(t, int32(100), it.Health.CriticalHealthPercent,
		"with no par2 volumes in the NZB a single missing article is already fatal")
	require.True(t, it.CanBeRemoved, "a failed usenet transfer has nothing left to keep")
}

func TestClientReportsAnEncryptedReleaseRatherThanUnpackingGarbage(t *testing.T) {
	// The *arr failed-download contract: an encrypted release is reported as
	// such so it can be blocklisted and re-searched. Writing ciphertext into
	// the library is the failure this prevents.
	srv := newStubServer(t)
	archive := encryptedZip(t)
	nzb := buildNZB(t, srv, "Locked.Release", []fileSpec{{name: "locked.zip", parts: [][]byte{archive}}})

	c, _, dataDir := newTestClient(t, Config{
		Providers:   []Provider{srv.provider("solo", 2, 1)},
		PostProcess: PostProcess{Unpack: true},
	})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Locked.Release", Payload: nzb})
	require.NoError(t, err)

	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status)
	require.True(t, it.IsEncrypted, "an encrypted release must be reported as encrypted")
	require.Equal(t, downloadv1alpha1.DownloadFailureEncrypted, it.FailureReason)
	require.NoDirExists(t, filepath.Join(dataDir, "default", "Locked.Release"),
		"nothing may be published from an archive that could not be opened")
}

// encryptedZip builds a zip whose only entry has the "encrypted" flag bit set.
// archive/zip cannot decrypt, so this is exactly what a password-protected
// release looks like to this client.
func encryptedZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "movie.mkv", Method: zip.Store, Flags: 0x1})
	require.NoError(t, err)
	_, err = w.Write([]byte("ciphertext-not-a-movie"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestClientUnpacksAZipRelease(t *testing.T) {
	srv := newStubServer(t)
	payload := []byte("the actual movie bytes, honest")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("movie.mkv")
	require.NoError(t, err)
	_, err = w.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	nzb := buildNZB(t, srv, "Packed", []fileSpec{{name: "packed.zip", parts: [][]byte{buf.Bytes()}}})

	c, scratch, dataDir := newTestClient(t, Config{
		Providers:   []Provider{srv.provider("solo", 2, 1)},
		PostProcess: PostProcess{Unpack: true, DeleteArchives: true},
	})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Packed", Payload: nzb})
	require.NoError(t, err)

	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)

	got, err := os.ReadFile(filepath.Join(dataDir, "default", "Packed", "movie.mkv"))
	require.NoError(t, err)
	require.Equal(t, payload, got)

	require.NoFileExists(t, filepath.Join(dataDir, "default", "Packed", "packed.zip"),
		"the archive itself must not be published")
	require.NoDirExists(t, filepath.Join(scratch, "default", id, "content"),
		"the scratch copy of an extracted archive must be released")
}

func TestClientStartsPausedAndResumes(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Paused", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(1, 400), partPayload(2, 400)}}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Paused", Payload: nzb, Paused: true})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		it, err := c.Get(context.Background(), id)
		return err == nil && it.Status == download.StatusPaused
	}, 5*time.Second, 10*time.Millisecond, "a transfer added paused must report Paused")

	require.NoError(t, c.Resume(context.Background(), id))
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
}

func TestClientLifecycleMethodsOnAnUnknownID(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}})
	ctx := context.Background()

	_, err := c.Get(ctx, "nope")
	require.ErrorIs(t, err, download.ErrNotFound)
	require.ErrorIs(t, c.Pause(ctx, "nope"), download.ErrNotFound)
	require.ErrorIs(t, c.Resume(ctx, "nope"), download.ErrNotFound)
	require.ErrorIs(t, c.MarkImported(ctx, "nope"), download.ErrNotFound)
	require.ErrorIs(t, c.Remove(ctx, "nope", true), download.ErrNotFound,
		"a finalizer reads ErrNotFound as already-gone, which is what makes Remove idempotent")

	// SetSeedCriteria is the documented exception: a usenet transfer never
	// seeds, so the goal is vacuously met and the controller must not have to
	// branch on protocol.
	require.NoError(t, c.SetSeedCriteria(ctx, "nope", commonv1alpha1.SeedCriteria{}))
}

func TestClientResumeOnARunningTransferIsANoOp(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Level", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(1, 400)}}})
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Level", Payload: nzb})
	require.NoError(t, err)
	// The controller is level-driven and cannot know the client's state.
	require.NoError(t, c.Resume(context.Background(), id))
	require.NoError(t, c.Resume(context.Background(), id))
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
}

func TestClientMarkImportedReleasesScratchAndAllowsRemoval(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Imported", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(1, 500)}}})

	c, scratch, dataDir := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Imported", Payload: nzb})
	require.NoError(t, err)
	waitForTerminal(t, c, id)

	require.NoError(t, c.MarkImported(context.Background(), id))
	it, err := c.Get(context.Background(), id)
	require.NoError(t, err)
	require.True(t, it.CanBeRemoved)

	// The published content is untouched; only the scratch area is released.
	require.FileExists(t, filepath.Join(dataDir, "default", "Imported", "movie.mkv"))
	require.FileExists(t, filepath.Join(scratch, "default", id, manifestName),
		"the manifest stays, because it is what keeps Add idempotent")

	require.NoError(t, c.Remove(context.Background(), id, true))
	_, err = c.Get(context.Background(), id)
	require.ErrorIs(t, err, download.ErrNotFound)
	require.NoDirExists(t, filepath.Join(dataDir, "default", "Imported"))
}

func TestClientRejectsAMagnet(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}})

	_, err := c.Add(context.Background(), download.AddRequest{Name: "x", Magnet: "magnet:?xt=urn:btih:deadbeef"})
	require.ErrorIs(t, err, ErrUnsupportedPayload)

	_, err = c.Add(context.Background(), download.AddRequest{Name: "x"})
	require.ErrorIs(t, err, ErrUnsupportedPayload)
}

func TestClientInfoReportsItself(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}})

	info, err := c.Info(context.Background())
	require.NoError(t, err)
	require.Equal(t, commonv1alpha1.ProtocolUsenet, info.Protocol)
	require.Equal(t, "nntp", info.Implementation)
	require.Greater(t, info.FreeBytes, int64(0), "free space comes from statfs on the data directory")
	require.Zero(t, info.Seeding, "a usenet transfer never seeds")
}

func TestNewRejectsAClientWithNoProviders(t *testing.T) {
	root := t.TempDir()
	_, err := New(Config{ScratchDir: filepath.Join(root, "s"), DataDir: filepath.Join(root, "d")})
	require.ErrorIs(t, err, ErrNoProviders)
}

func TestProviderFromSpecAppliesTheCRDDefaults(t *testing.T) {
	quota := int64(500 << 30)
	p := ProviderFromSpec(downloadv1alpha1.NNTPProvider{
		Name: "news", Host: " news.example ", QuotaBytes: &quota, Priority: 3, Backup: true,
	}, "user", "pass")

	require.Equal(t, "news.example", p.Host)
	require.True(t, p.TLS)
	require.Equal(t, 563, p.Port, "TLS providers default to 563")
	require.Equal(t, 8, p.Connections)
	require.Equal(t, quota, p.QuotaBytes)
	require.True(t, p.Backup)
	require.Equal(t, int32(3), p.Priority)

	plain := false
	p = ProviderFromSpec(downloadv1alpha1.NNTPProvider{Name: "n", Host: "h", TLS: &plain}, "", "")
	require.Equal(t, 119, p.Port, "plaintext providers default to 119")
}

func TestClientRepairsAReleaseWithAMissingArticle(t *testing.T) {
	// The whole pipeline, with real par2: an article the provider cannot
	// serve leaves a hole in the file, par2cmdline rebuilds it from the
	// recovery volumes in the same NZB, and what reaches the library is
	// byte-identical to what was posted.
	bin := par2Binary(t)

	// Build the release on disk first, so par2 sees the real bytes.
	stage := t.TempDir()
	content := make([]byte, 96<<10)
	rnd := rand.New(rand.NewSource(42))
	_, _ = rnd.Read(content)
	require.NoError(t, os.WriteFile(filepath.Join(stage, "movie.mkv"), content, 0o644))

	create := exec.Command(bin, "c", "-q", "-b16", "-r50", "--", "movie.mkv.par2", "movie.mkv")
	create.Dir = stage
	out, err := create.CombinedOutput()
	require.NoErrorf(t, err, "par2 create failed: %s", out)

	entries, err := os.ReadDir(stage)
	require.NoError(t, err)

	srv := newStubServer(t)
	// The content file in four parts, so exactly one can go missing.
	const parts = 4
	partSize := len(content) / parts
	movieParts := make([][]byte, parts)
	for i := range parts {
		end := (i + 1) * partSize
		if i == parts-1 {
			end = len(content)
		}
		movieParts[i] = content[i*partSize : end]
	}
	specs := []fileSpec{{name: "movie.mkv", parts: movieParts}}
	for _, e := range entries {
		if e.Name() == "movie.mkv" {
			continue
		}
		body, rErr := os.ReadFile(filepath.Join(stage, e.Name()))
		require.NoError(t, rErr)
		specs = append(specs, fileSpec{name: e.Name(), parts: [][]byte{body}})
	}
	require.Greater(t, len(specs), 2, "par2 should have produced recovery volumes")

	nzb := buildNZB(t, srv, "Damaged.Release", specs)
	// Not the first article: the first carries the yEnc header that sizes the
	// file, and losing it is a different (also handled) case.
	srv.refuse["f0-p2@clustarr.test"] = 430

	c, _, dataDir := newTestClient(t, Config{
		Providers:          []Provider{srv.provider("solo", 4, 1)},
		Par2Path:           bin,
		PostProcess:        PostProcess{Par2: true, DeleteArchives: true},
		AbortHealthPercent: 50,
	})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Damaged.Release", Payload: nzb})
	require.NoError(t, err)

	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.NotNil(t, it.Health)
	require.Equal(t, int32(1), it.Health.FailedArticles)

	published := filepath.Join(dataDir, "default", "Damaged.Release")
	got, err := os.ReadFile(filepath.Join(published, "movie.mkv"))
	require.NoError(t, err)
	require.Equal(t, content, got, "par2 must have rebuilt the missing article's bytes")

	left, err := os.ReadDir(published)
	require.NoError(t, err)
	names := make([]string, 0, len(left))
	for _, e := range left {
		names = append(names, e.Name())
	}
	require.Equal(t, []string{"movie.mkv"}, names,
		"neither the par2 volumes nor par2cmdline's damaged-file backup may reach the library")
}

func TestARestartResumesFromTheCheckpointedSegments(t *testing.T) {
	// The crash-survival claim, made observable: the manifest's per-file
	// bitsets are what stop a restart re-fetching a 50GB release from the
	// first article, so a job whose first two segments are already recorded
	// must ask the provider only for the rest.
	srv := newStubServer(t)
	parts := [][]byte{partPayload(1, 400), partPayload(2, 400), partPayload(3, 400), partPayload(4, 400)}
	nzb := buildNZB(t, srv, "Half.Done", []fileSpec{{name: "movie.mkv", parts: parts}})

	root := t.TempDir()
	cfg := Config{
		Providers:  []Provider{srv.provider("solo", 2, 1)},
		ScratchDir: filepath.Join(root, "scratch"),
		DataDir:    filepath.Join(root, "data"),
	}

	seed, err := New(cfg)
	require.NoError(t, err)
	client := seed.(*Client)

	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	dir := filepath.Join(cfg.ScratchDir, "default", parsed.ID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, nzbName), nzb, 0o644))

	j := client.newJob(parsed.ID, "Half.Done", "default", dir, parsed, nzb)
	j.status = download.StatusDownloading
	j.done[0].set(0)
	j.done[0].set(1)
	require.NoError(t, j.checkpoint())
	require.NoError(t, seed.Close())

	// A fresh client re-attaches and finishes the job.
	resumed, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resumed.Close() })
	waitForTerminal(t, resumed, parsed.ID)

	require.Zero(t, srv.servedCount("f0-p0@clustarr.test"), "segment 0 was already on disk")
	require.Zero(t, srv.servedCount("f0-p1@clustarr.test"), "segment 1 was already on disk")
	require.Equal(t, 1, srv.servedCount("f0-p2@clustarr.test"))
	require.Equal(t, 1, srv.servedCount("f0-p3@clustarr.test"))
}

// TestConcurrentCheckpointsNeverCollide is the regression test for a race CI
// found: checkpoint used to snapshot under mu and write outside any lock, so
// two concurrent checkpoints (Resume beside the transfer's own progress write)
// raced on fsops.AtomicWrite's single "manifest.json.partial" name -- one
// rename moved the other's temp file away and the loser failed with ENOENT.
func TestConcurrentCheckpointsNeverCollide(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Race", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(1, 400)}}})
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Race", Payload: nzb})
	require.NoError(t, err)
	j, err := c.lookup(id)
	require.NoError(t, err)

	const writers = 16
	errs := make(chan error, writers*20)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				errs <- j.checkpoint()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	waitForTerminal(t, c, id)
}
