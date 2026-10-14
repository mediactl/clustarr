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

package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestEngineRuntimeCarriesTheControllerOptions holds the link from the
// grab manager's options and environment to what the DownloadClient
// controller stamps onto every engine pod (X14): the engine ServiceAccount,
// this process's bus address, and $UMASK. Each has a legal empty value that
// fails only on a real node, where envtest never schedules one, so the value
// itself is the only proof it arrives. downloadclient's
// TestEnginePodsGetTheRuntimeTheyNeed holds the next link, the runtime into
// the pod spec. (The engine joins the manager's JetStream and waits for the
// topology the manager ensures, so no single-node setting is stamped; the
// flag default and its validation live in internal/cli.)
func TestEngineRuntimeCarriesTheControllerOptions(t *testing.T) {
	t.Setenv("UMASK", "002")
	o := Options{
		Options:              k8s.Options{NATSURL: "nats://nats.media.svc:4222"},
		EngineServiceAccount: "media-clustarr-grab-engine",
		// --engine-memory-limit-torrent and -usenet (smoke finding O9).
		EngineMemoryLimitTorrent: resource.MustParse("3Gi"),
		EngineMemoryLimitUsenet:  resource.MustParse("6Gi"),
	}

	require.Equal(t, downloadclient.EngineRuntime{
		ServiceAccountName: "media-clustarr-grab-engine",
		NATSURL:            "nats://nats.media.svc:4222",
		Umask:              "002",
		TorrentMemoryLimit: resource.MustParse("3Gi"),
		UsenetMemoryLimit:  resource.MustParse("6Gi"),
	}, engineRuntime(o))
}
