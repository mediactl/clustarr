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
	"fmt"
	"strings"
)

// EngineIdentity is the torrent engine's "<client>-<ordinal>" (spec §3.5.5),
// the engine label its Downloads carry. podName must be the StatefulSet
// replica "<client>-engine-<N>" with N decimal: anything else fails at start
// rather than claiming ordinal "abc12". The DownloadClient controller names
// the StatefulSet "<client>-engine", so the client's own hyphens never
// confuse the split.
func EngineIdentity(client, podName string) (string, error) {
	n, ok := strings.CutPrefix(podName, client+"-engine-")
	if client == "" || !ok || n == "" || strings.Trim(n, "0123456789") != "" {
		return "", fmt.Errorf("torrent engine: pod %q is not a replica of DownloadClient %q's StatefulSet (%s-engine-<N>)",
			podName, client, client)
	}
	return client + "-" + n, nil
}

// PodName is $POD_NAME, else the hostname (which a StatefulSet pod's is).
func PodName(getenv func(string) string, hostname func() (string, error)) (string, error) {
	if p := getenv("POD_NAME"); p != "" {
		return p, nil
	}
	return hostname()
}
