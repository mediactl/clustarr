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

package torrent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// TestLoadDescriptorsOnAFreshStateDirIsEmptyNotAnError proves a brand new
// engine, which has never persisted anything, re-attaches zero transfers
// rather than failing.
func TestLoadDescriptorsOnAFreshStateDirIsEmptyNotAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	got, errs := loadDescriptors(dir)
	assert.Empty(t, got)
	assert.Empty(t, errs)
}

func TestSaveLoadRemoveDescriptorRoundTrips(t *testing.T) {
	dir := t.TempDir()
	ratio := resource.MustParse("2.0")
	seedTime := metav1.Duration{Duration: 3600}
	d := descriptor{
		Name:             "my-movie",
		Category:         "movies",
		ExpectedInfoHash: "0123456789abcdef0123456789abcdef01234567",
		Priority:         downloadv1alpha1.DownloadPriorityHigh,
		Paused:           true,
		SeedCriteria:     &commonv1alpha1.SeedCriteria{Ratio: &ratio, SeedTime: &seedTime},
	}
	payload := []byte("d8:announce...fake metainfo bytes")

	require.NoError(t, saveDescriptor(dir, "abc123", payload, d))

	// Both files exist at the documented paths.
	assert.FileExists(t, filepath.Join(dir, "abc123.torrent"))
	assert.FileExists(t, filepath.Join(dir, "abc123.json"))

	got, errs := loadDescriptors(dir)
	require.Empty(t, errs)
	require.Len(t, got, 1)
	assert.Equal(t, "abc123", got[0].ID)
	assert.Equal(t, payload, got[0].Payload)
	assert.Equal(t, d.Name, got[0].Desc.Name)
	assert.Equal(t, d.Category, got[0].Desc.Category)
	assert.True(t, got[0].Desc.Paused)
	require.NotNil(t, got[0].Desc.SeedCriteria)
	assert.Equal(t, "2", got[0].Desc.SeedCriteria.Ratio.String())

	// addRequest renders it back faithfully.
	req := got[0].addRequest()
	assert.Equal(t, "my-movie", req.Name)
	assert.Equal(t, "movies", req.Category)
	assert.Equal(t, payload, req.Payload)
	assert.True(t, req.Paused)
	assert.Equal(t, downloadv1alpha1.DownloadPriorityHigh, req.Priority)

	require.NoError(t, removeDescriptor(dir, "abc123"))
	got, errs = loadDescriptors(dir)
	assert.Empty(t, errs)
	assert.Empty(t, got)
}

// TestSaveDescriptorMagnetOnlyWritesNoTorrentFile proves a magnet-sourced
// transfer persists without a .torrent file at all -- doc.go's own claim.
func TestSaveDescriptorMagnetOnlyWritesNoTorrentFile(t *testing.T) {
	dir := t.TempDir()
	d := descriptor{Name: "magnet-movie", Magnet: "magnet:?xt=urn:btih:deadbeef"}
	require.NoError(t, saveDescriptor(dir, "deadbeef", nil, d))

	assert.NoFileExists(t, filepath.Join(dir, "deadbeef.torrent"))
	assert.FileExists(t, filepath.Join(dir, "deadbeef.json"))

	got, errs := loadDescriptors(dir)
	require.Empty(t, errs)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Payload)
	assert.Equal(t, "magnet:?xt=urn:btih:deadbeef", got[0].Desc.Magnet)
}

// TestRemoveDescriptorOfUnknownIDIsIdempotent mirrors
// download.Client.Remove's own idempotency contract: removing something that
// was never there, or already removed, is not an error.
func TestRemoveDescriptorOfUnknownIDIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, removeDescriptor(dir, "never-existed"))
}

// TestUpdateDescriptorStateRefreshesPausedAndSeedCriteria proves a
// mid-lifecycle pause/seed-criteria change is persisted, so a restart
// re-attaches in the CURRENT state rather than the state Add originally saw.
func TestUpdateDescriptorStateRefreshesPausedAndSeedCriteria(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, saveDescriptor(dir, "id1", nil, descriptor{Name: "movie", Magnet: "magnet:?xt=urn:btih:id1"}))

	ratio := resource.MustParse("1.5")
	require.NoError(t, updateDescriptorState(dir, "id1", true, &commonv1alpha1.SeedCriteria{Ratio: &ratio}))

	got, errs := loadDescriptors(dir)
	require.Empty(t, errs)
	require.Len(t, got, 1)
	assert.True(t, got[0].Desc.Paused)
	require.NotNil(t, got[0].Desc.SeedCriteria)
	// resource.Quantity re-canonicalises on a JSON round trip (DecimalSI
	// "1.5" becomes "1500m"), so compare by value via Cmp, not by String().
	assert.Zero(t, got[0].Desc.SeedCriteria.Ratio.Cmp(ratio))
}

// TestUpdateDescriptorStateOnUnknownIDIsANoOp proves refreshing a descriptor
// that was already removed (or never existed) does not fabricate one --
// updateDescriptorState must never resurrect a removed transfer's state.
func TestUpdateDescriptorStateOnUnknownIDIsANoOp(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, updateDescriptorState(dir, "never-existed", true, nil))
	got, errs := loadDescriptors(dir)
	assert.Empty(t, errs)
	assert.Empty(t, got)
}

// TestLoadDescriptorsSkipsACorruptEntryAndKeepsTheRest proves one damaged
// sidecar does not strand every other transfer's re-attach behind it (this
// package's own doc comment on loadDescriptors).
func TestLoadDescriptorsSkipsACorruptEntryAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, saveDescriptor(dir, "good", nil, descriptor{Name: "good-one", Magnet: "magnet:?xt=urn:btih:good"}))

	// A corrupt sidecar: not valid JSON.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o640))

	got, errs := loadDescriptors(dir)
	require.Len(t, errs, 1, "exactly the corrupt entry should have failed to load")
	require.Len(t, got, 1, "the good entry must still load")
	assert.Equal(t, "good", got[0].ID)
}
