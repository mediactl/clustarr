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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	"github.com/mediactl/clustarr/pkg/binpath"
)

// TestEngineArgvParses: every argv the manager's DownloadClient controller
// renders for an engine pod parses through this binary into the engine
// domain it names (§10.3.1). envtest runs no pods, so nothing else would
// notice an engine argv the agent refuses: the pod would crash-loop on the
// first real cluster.
func TestEngineArgvParses(t *testing.T) {
	torrent := &downloadv1alpha1.DownloadClient{ObjectMeta: metav1.ObjectMeta{Name: "my-torrent-client"},
		Spec: downloadv1alpha1.DownloadClientSpec{Torrent: &downloadv1alpha1.TorrentSpec{
			Scratch: &downloadv1alpha1.ScratchSpec{Path: "/data/torrents/incomplete"}}}}
	plainTorrent := &downloadv1alpha1.DownloadClient{ObjectMeta: metav1.ObjectMeta{Name: "seedbox"},
		Spec: downloadv1alpha1.DownloadClientSpec{Torrent: &downloadv1alpha1.TorrentSpec{}}}
	usenet := &downloadv1alpha1.DownloadClient{ObjectMeta: metav1.ObjectMeta{Name: "nzb"},
		Spec: downloadv1alpha1.DownloadClientSpec{Usenet: &downloadv1alpha1.UsenetSpec{PublishDir: "/data/usenet/complete"}}}
	scratchUsenet := &downloadv1alpha1.DownloadClient{ObjectMeta: metav1.ObjectMeta{Name: "my-nzb"},
		Spec: downloadv1alpha1.DownloadClientSpec{Usenet: &downloadv1alpha1.UsenetSpec{
			Scratch: &downloadv1alpha1.ScratchSpec{Path: "/data/usenet/incomplete"}}}}

	t.Setenv("POD_NAME", "my-torrent-client-engine-1")
	cmd, args := downloadclient.EngineCommand(torrent, "/data", "/scratch")
	require.Equal(t, []string{binpath.Agent}, cmd)
	o, err := parse(t, args...)
	require.NoError(t, err, "agent %v", args)
	assert.Equal(t, "torrent-engine", o.Domain)
	assert.Equal(t, "my-torrent-client-1", o.Engine, "the ordinal comes from the StatefulSet pod's name")
	assert.Equal(t, "/data", o.DataDir)
	assert.Equal(t, "/data/torrents/incomplete", o.ScratchDir)

	t.Setenv("POD_NAME", "seedbox-engine-0")
	_, args = downloadclient.EngineCommand(plainTorrent, "/data", "/scratch")
	o, err = parse(t, args...)
	require.NoError(t, err, "agent %v", args)
	assert.Equal(t, "seedbox-0", o.Engine)

	cmd, args = downloadclient.EngineCommand(usenet, "/data", "/scratch")
	require.Equal(t, []string{binpath.Agent}, cmd)
	o, err = parse(t, args...)
	require.NoError(t, err, "agent %v", args)
	assert.Equal(t, "usenet-engine", o.Domain)
	assert.Equal(t, "nzb-0", o.Engine)
	assert.Equal(t, "/data", o.DataDir)
	assert.Equal(t, "/scratch", o.ScratchDir)
	assert.Equal(t, "/data/usenet/complete", o.PublishDir)

	_, args = downloadclient.EngineCommand(scratchUsenet, "/data", "/scratch")
	o, err = parse(t, args...)
	require.NoError(t, err, "agent %v", args)
	assert.Equal(t, "my-nzb-0", o.Engine)
	assert.Equal(t, "/data/usenet/incomplete", o.ScratchDir)
	assert.Empty(t, o.PublishDir, "empty publishes under --data-dir")
}
