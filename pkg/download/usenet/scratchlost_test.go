/*
Copyright 2026 The clustarr Authors.

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
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// On talos (2026-10-27) /data/usenet/incomplete/tv was removed from the NAS
// side while the engine transferred a 26 GB season pack. NFSv4 kept the
// open files writable, so every article "landed"; the end-of-transfer chase
// then reopened the targets with O_CREATE, which recreated the job directory
// with twelve empty files, and the publish moved those into the data
// directory as a completed download. These hold the three guards against it:
// a retry never creates a target, a checkpoint that finds no scratch
// directory fails the transfer, and nothing publishes whose size is not
// what its yEnc header declared.

// pausedJob adds a one-file NZB paused, so its transfer parks before it
// opens any target, and returns the job and its id.
func pausedJob(t *testing.T, srv *stubServer, c *Client, title string, payload []byte) (*job, string) {
	t.Helper()
	nzb := buildNZB(t, srv, title, []fileSpec{{name: "movie.mkv", parts: [][]byte{payload}}})
	id, err := c.Add(context.Background(), download.AddRequest{Name: title, Payload: nzb, Category: "movies", Paused: true})
	require.NoError(t, err)
	j, err := c.lookup(id)
	require.NoError(t, err)
	// The job's start checkpoints its manifest (setStage) before it parks
	// in the slot queue; a test that removes its scratch waits for that,
	// or the write lands in the middle of the removal.
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, parked := c.waiting[j]
		return parked
	}, 5*time.Second, time.Millisecond, "the paused job parks for its turn")
	return j, id
}

func TestARetryDoesNotRecreateATargetThatVanished(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	j, _ := pausedJob(t, srv, c, "Vanished.Scratch", partPayload(1, 900))

	// The transfer had opened its targets; then the whole category
	// directory went, as it did on talos.
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.contentDir(), "movie.mkv"), make([]byte, 900), 0o644))
	require.NoError(t, os.RemoveAll(filepath.Dir(j.dir)))

	_, err := j.fetchMissing(context.Background(), nil, 1, []seg{{0, 0}})
	require.ErrorIs(t, err, ErrScratchLost)
	require.NoDirExists(t, j.contentDir(), "a retry must not recreate the files it retries into")
}

func TestACheckpointWithNoScratchDirectoryFailsTheTransfer(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	j, _ := pausedJob(t, srv, c, "Gone.Scratch", partPayload(1, 900))
	require.NoError(t, os.RemoveAll(filepath.Dir(j.dir)))

	var (
		mu  sync.Mutex
		got error
	)
	stop := j.startCheckpoint(context.Background(), func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if got == nil {
			got = err
		}
	})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got != nil
	}, 8*time.Second, 50*time.Millisecond, "the checkpoint ticker never failed the transfer")
	stop()
	mu.Lock()
	defer mu.Unlock()
	require.ErrorIs(t, got, ErrScratchLost)
}

// transferredJob makes j look like a transfer that landed every article of
// its one file, whose yEnc header declared size bytes, with onDisk bytes
// actually in the content directory.
func transferredJob(t *testing.T, j *job, size, onDisk int) {
	t.Helper()
	j.mu.Lock()
	for si := range j.nzb.Files[0].Segments {
		j.done[0].set(si)
	}
	j.sizes[0] = int64(size)
	j.mu.Unlock()
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.contentDir(), "movie.mkv"), make([]byte, onDisk), 0o644))
}

func TestPostProcessRefusesAFileShorterThanItsHeaderSaid(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	j, _ := pausedJob(t, srv, c, "Short.File", partPayload(1, 900))
	transferredJob(t, j, 900, 100)

	err := j.postProcess(context.Background())
	require.ErrorIs(t, err, ErrScratchLost)
	require.ErrorContains(t, err, "movie.mkv")
	require.ErrorContains(t, err, "900")
	require.NoDirExists(t, filepath.Join(c.cfg.PublishDir, "movies", "Short.File"), "a short file is never published")
	require.Equal(t, commonv1alpha1.DownloadFailureWriteError, failureReason(context.Background(), err),
		"a local fault, never a verdict on the release")
}

func TestPostProcessPublishesAFileAtItsDeclaredSize(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	j, _ := pausedJob(t, srv, c, "Whole.File", partPayload(1, 900))
	transferredJob(t, j, 900, 900)

	require.NoError(t, j.postProcess(context.Background()))
	require.FileExists(t, filepath.Join(c.cfg.PublishDir, "movies", "Whole.File", "movie.mkv"))
}

func TestADownloadRecordsEachFilesDecodedSizeInItsManifest(t *testing.T) {
	srv := newStubServer(t)
	parts := [][]byte{partPayload(1, 900), partPayload(2, 512)}
	nzb := buildNZB(t, srv, "Sized.Movie", []fileSpec{{name: "movie.mkv", parts: parts}})
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 4, 1)}})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Sized.Movie", Payload: nzb, Category: "movies"})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)

	j, err := c.lookup(id)
	require.NoError(t, err)
	loaded, err := c.loadJob(j.dir)
	require.NoError(t, err)
	require.Equal(t, []int64{1412}, loaded.sizes, "the manifest carries the size the yEnc header declared")
}

func TestRemoveSucceedsWhenTheScratchIsAlreadyGone(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	j, id := pausedJob(t, srv, c, "Gone.Before.Remove", partPayload(1, 900))
	require.NoError(t, os.RemoveAll(filepath.Dir(j.dir)))

	require.NoError(t, c.Remove(context.Background(), id, false), "a scratch directory already gone is nothing to remove")
	_, err := c.Get(context.Background(), id)
	require.ErrorIs(t, err, download.ErrNotFound)
}
