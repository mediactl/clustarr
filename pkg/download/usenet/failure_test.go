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

// Gap fix Y2: every DownloadFailureReason a usenet job can observe, and the
// outage that must not become one.
package usenet

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestFailureReasonClassifiesEveryPipelineError(t *testing.T) {
	writeErr := func(errno syscall.Errno) error {
		return fmt.Errorf("usenet: write movie.mkv at 0: %w", &fs.PathError{Op: "write", Path: "/scratch/x", Err: errno})
	}
	live := context.Background()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want downloadv1alpha1.DownloadFailureReason
	}{
		{"health floor crossed", live, fmt.Errorf("%w: health 50%%", ErrUnrecoverable), downloadv1alpha1.DownloadFailureMissingArticles},
		{"pre-check verdict", live, fmt.Errorf("%w: pre-check found 3 of 4 articles missing", ErrUnrecoverable), downloadv1alpha1.DownloadFailureMissingArticles},
		{"par2 could not repair", live, fmt.Errorf("%w: need 12 more blocks", ErrRepairFailed), downloadv1alpha1.DownloadFailureMissingArticles},
		{"encrypted archive", live, fmt.Errorf("%w: movie.rar", ErrEncrypted), downloadv1alpha1.DownloadFailureEncrypted},
		{"statfs guard", live, fmt.Errorf("fsops: %w", fsops.ErrInsufficientSpace), downloadv1alpha1.DownloadFailureDiskFull},
		{"ENOSPC on a write", live, writeErr(syscall.ENOSPC), downloadv1alpha1.DownloadFailureDiskFull},
		{"EDQUOT on a write", live, writeErr(syscall.EDQUOT), downloadv1alpha1.DownloadFailureDiskFull},
		{"EIO on a write", live, writeErr(syscall.EIO), downloadv1alpha1.DownloadFailureWriteError},
		{"no par2 binary is a local fault", live, fmt.Errorf("%w: 3 articles missing", ErrPar2Unavailable), downloadv1alpha1.DownloadFailureWriteError},
		{"deadline", live, context.DeadlineExceeded, downloadv1alpha1.DownloadFailureTimeout},
		// A par2 child killed by the deadline reports its own failure; the
		// expired context decides, so it is not blocklisted as unrepairable.
		{"repair killed by the deadline", expired, fmt.Errorf("%w: signal: killed", ErrRepairFailed), downloadv1alpha1.DownloadFailureTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, failureReason(tc.ctx, tc.err))
		})
	}
}

// A provider that rejects the credentials makes every article unavailable.
// Before Y2 that read as "every article missing" -- a missingArticles
// failure, which now blocklists -- so a wrong password would have
// blocklisted every release grabbed while it stood. The job must wait
// instead.
func TestClientWaitsForAProviderInsteadOfFailingTheRelease(t *testing.T) {
	srv := newStubServer(t)
	parts := [][]byte{partPayload(1, 400), partPayload(2, 400)}
	nzb := buildNZB(t, srv, "Locked.Out", []fileSpec{{name: "movie.mkv", parts: parts}})
	srv.requireAuth = true
	srv.user, srv.pass = "clustarr", "s3cret"
	p := srv.provider("solo", 2, 1)
	p.Password = "wrong"

	c, _, _ := newTestClient(t, Config{Providers: []Provider{p}, ProviderRetryDelay: 20 * time.Millisecond})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Locked.Out", Payload: nzb})
	require.NoError(t, err)

	var it download.Item
	require.Eventually(t, func() bool {
		it, err = c.Get(context.Background(), id)
		require.NoError(t, err)
		return it.Status == download.StatusWarning
	}, 10*time.Second, 10*time.Millisecond, "never reported waiting; last %+v", it)
	assert.Contains(t, it.Message, "waiting for a usenet provider")

	require.Never(t, func() bool {
		it, err = c.Get(context.Background(), id)
		require.NoError(t, err)
		return it.Status == download.StatusFailed
	}, 500*time.Millisecond, 10*time.Millisecond, "an unreachable provider failed the release: %s", it.FailureReason)
	assert.False(t, it.FailureReason.IsFailure())
	require.NotNil(t, it.Health)
	assert.Zero(t, it.Health.FailedArticles, "an article nobody could be asked for is not a failed article")
}

// A job still running at its DownloadTimeout fails as timeout -- here while
// it waits on a provider that never comes back.
func TestClientFailsAJobPastItsDownloadTimeout(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Too.Slow", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(1, 400)}}})
	srv.greetBusy = true

	c, _, _ := newTestClient(t, Config{
		Providers:          []Provider{srv.provider("solo", 2, 1)},
		DownloadTimeout:    300 * time.Millisecond,
		ProviderRetryDelay: time.Hour,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Too.Slow", Payload: nzb})
	require.NoError(t, err)

	it := waitForTerminal(t, c, id)
	assert.Equal(t, download.StatusFailed, it.Status)
	assert.Equal(t, downloadv1alpha1.DownloadFailureTimeout, it.FailureReason)
	assert.True(t, strings.Contains(it.Message, "deadline"), "message %q", it.Message)
}
