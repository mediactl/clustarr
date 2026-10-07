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

	"github.com/mediactl/clustarr/app/caption/providerset/build"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/ffruntime/ffmetrics"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	embeddednative "github.com/mediactl/clustarr/pkg/subtitles/providers/embedded/native"
)

// extractNeeds is what extraction asks of FFmpeg; a variable for one test.
var extractNeeds = embeddednative.Needs

// registerExtraction gives the provider builder the in-process extractor
// (spec §7.3.1), adds the process-level healthz check "ffgo" and the
// abandoned-calls gauge, and routes FFmpeg's log. Without FFmpeg 9 and the
// srt encoder the domain does not start.
func registerExtraction(ctx context.Context, b *build.Builder, live *k8s.Checks) error {
	if err := ffruntime.Require(extractNeeds); err != nil {
		return fmt.Errorf("caption: embedded subtitle extraction is unavailable: %w", err)
	}
	if err := live.Add("ffgo", ffruntime.Healthz); err != nil {
		return err
	}
	if err := ffmetrics.Register(ctrlmetrics.Registry); err != nil {
		return err
	}
	ffruntime.RouteLog(logging.FromContext(ctx))
	b.Extract = embeddednative.Extract
	return nil
}
