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

package grabarr

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/grabarr/controller/downloadclient"
)

// TestEngineRuntimeCarriesTheControllerOptions holds the link from
// grabarr's options and environment to what the DownloadClient controller
// stamps onto every engine pod (X14): the engine ServiceAccount, this
// process's bus address and single-node setting, and $UMASK. Each has a
// legal empty value that fails only on a real node, where envtest never
// schedules one, so the value itself is the only proof it arrives.
// downloadclient's TestEnginePodsGetTheRuntimeTheyNeed holds the next link,
// the runtime into the pod spec.
func TestEngineRuntimeCarriesTheControllerOptions(t *testing.T) {
	t.Setenv("UMASK", "002")
	o := DefaultOptions()
	o.EngineServiceAccount = "media-clustarr-grabarr-engine"
	o.NATSURL = "nats://nats.media.svc:4222"
	o.BusSingleNode = true

	require.Equal(t, downloadclient.EngineRuntime{
		ServiceAccountName: "media-clustarr-grabarr-engine",
		NATSURL:            "nats://nats.media.svc:4222",
		BusSingleNode:      true,
		Umask:              "002",
	}, engineRuntime(o))

	require.Equal(t, downloadclient.DefaultEngineServiceAccount, DefaultOptions().EngineServiceAccount,
		"config/'s engine ServiceAccount is the flag's default")

	o.EngineImage = "img"
	o.EngineServiceAccount = ""
	require.ErrorContains(t, o.Validate(), "--engine-service-account",
		"a controller that would stamp no ServiceAccount onto its engines was accepted")
}
