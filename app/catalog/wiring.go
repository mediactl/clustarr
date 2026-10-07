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

package catalogarr

import (
	"net/http"
	"time"
)

// defaultHTTPClient is the outbound client the MetadataProvider reconciler
// probes with. It is a named value rather than http.DefaultClient so a
// misbehaving provider cannot hang a reconcile for the manager's whole
// five-minute ReconciliationTimeout.
var defaultHTTPClient = &http.Client{Timeout: metadataProbeTimeout}

// metadataProbeTimeout bounds one MetadataProvider credential probe.
const metadataProbeTimeout = 30 * time.Second
