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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// TestATransientImportIsRetriedThenHeld plays every delivery of an import
// whose content root cannot be read: the owner's three retries over about
// an hour (2026-10-07), status.import pending and saying when, and after
// the last a hold for a person -- never a failure grabarr would blocklist.
func TestATransientImportIsRetriedThenHeld(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-remediate-transient")
	movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}
	unpublished := filepath.Join(dataDir(t, "scratch"), "not-there")
	dl := f.createDownloadWith(t, "transient-dl", unpublished, movie, "", nil, grabbedAs(downloadv1alpha1.GrabSourceSearch, false))

	backoff := []time.Duration{time.Minute, 10 * time.Minute, 45 * time.Minute}
	for attempt := uint64(1); attempt <= 3; attempt++ {
		m := newImportTaskMessage(t, f.ns, dl.Name, "")
		m.attempt = attempt
		before := time.Now()
		var retry *events.RetryError
		require.ErrorAs(t, f.worker.Handle(ctx, m), &retry, "attempt %d is retried", attempt)
		assert.Zero(t, retry.After, "on the consumer's own backoff")

		got := f.importState(t, dl).Status.Import
		assert.Equal(t, downloadv1alpha1.ImportPhasePending, got.State, "attempt %d", attempt)
		assert.Equal(t, downloadv1alpha1.ImportClassTransient, got.Class)
		assert.EqualValues(t, attempt, got.Attempts)
		require.NotNil(t, got.NextAttemptAt, "attempt %d says when the next is", attempt)
		assert.WithinDuration(t, before.Add(backoff[attempt-1]), got.NextAttemptAt.Time, 5*time.Second)
		assert.Nil(t, got.HeldSince)
		assert.Contains(t, got.Message, "is not accessible")
	}

	last := newImportTaskMessage(t, f.ns, dl.Name, "")
	last.attempt = 4
	require.NoError(t, f.worker.Handle(ctx, last), "the last delivery holds the import rather than failing it")
	got := f.importState(t, dl).Status.Import
	assert.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State)
	assert.Equal(t, downloadv1alpha1.ImportClassTransient, got.Class)
	assert.EqualValues(t, 4, got.Attempts)
	require.NotNil(t, got.HeldSince, "held for a person")
	assert.Nil(t, got.NextAttemptAt, "no attempt is pending")
	assert.Contains(t, got.Message, "held for a person until")
}

// TestARefusedReleaseIsRecheckedBeforeItIsBlamed: a movie download whose
// one file the profile does not allow is the release's fault, built through
// the real walk -- but walked once more first, and blocked as such only when
// the second walk agrees. Its file stays: removing it is grabarr's and the
// engine's, once the release is blocklisted.
func TestARefusedReleaseIsRecheckedBeforeItIsBlamed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-remediate-fault")
	movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}
	root := dataDir(t, "scratch")
	file := filepath.Join(root, "The.Matrix.1999.720p.BluRay.x264-GRP.mkv")
	mustWriteSparseFile(t, file, sampleFloor)
	dl := f.createDownloadWith(t, "fault-dl", root, movie, "", nil, grabbedAs(downloadv1alpha1.GrabSourceSearch, false))

	var retry *events.RetryError
	require.ErrorAs(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")), &retry,
		"the first refusal is checked again before the release is blamed")
	got := f.importState(t, dl).Status.Import
	assert.Equal(t, downloadv1alpha1.ImportPhasePending, got.State)
	assert.Equal(t, downloadv1alpha1.ImportClassReleaseFault, got.Class)
	require.NotNil(t, got.NextAttemptAt)
	require.Len(t, got.Rejections, 1)
	assert.Contains(t, got.Rejections[0], "quality Bluray-720p is not allowed by the quality profile")

	second := newImportTaskMessage(t, f.ns, dl.Name, "")
	second.attempt = 2
	require.NoError(t, f.worker.Handle(ctx, second))
	got = f.importState(t, dl).Status.Import
	assert.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State)
	assert.Equal(t, downloadv1alpha1.ImportClassReleaseFault, got.Class)
	assert.Equal(t, downloadv1alpha1.ImportMessageEveryFileRejected, got.Message)
	assert.Nil(t, got.HeldSince, "a release fault is blocklisted, not held")
	assert.Nil(t, got.NextAttemptAt)
	_, err := os.Stat(file)
	assert.NoError(t, err, "the importer never removes a download's files")
}
