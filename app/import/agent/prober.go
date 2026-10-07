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

package agent

import (
	"context"
	"fmt"

	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/ffruntime/ffmetrics"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// newProber is the import domain's prober; a variable for one test.
var newProber = func() (mediainfo.Prober, error) { return native.New() }

// registerProbe builds the domain's one in-process prober (spec §6.6), adds
// the process-level healthz check "ffgo" (§3.3) and the abandoned-calls
// gauge, and routes FFmpeg's log. Without FFmpeg 9 and the shim the domain
// does not start: the native image always carries them.
func registerProbe(ctx context.Context, live *k8s.Checks) (mediainfo.Prober, error) {
	p, err := newProber()
	if err != nil {
		return nil, fmt.Errorf("import: the in-process probe is unavailable: %w", err)
	}
	if err := live.Add("ffgo", ffruntime.Healthz); err != nil {
		return nil, err
	}
	if err := ffmetrics.Register(ctrlmetrics.Registry); err != nil {
		return nil, err
	}
	ffruntime.RouteLog(logging.FromContext(ctx))
	return p, nil
}
