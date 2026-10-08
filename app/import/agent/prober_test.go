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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
)

// TestTheImportDomainProbesInProcess (W8.6, spec 2026-10-06 §6.6, §3.3): the
// domain's one prober is the in-process native one, and the domain serves
// the healthz check "ffgo", so a wedged probe restarts the pod. It skips
// without FFmpeg 9 and its shim, which the native image always carries.
func TestTheImportDomainProbesInProcess(t *testing.T) {
	if _, err := native.New(); err != nil {
		t.Skipf("no FFmpeg 9 with its shim: %v", err)
	}
	var live k8s.Checks
	p, err := registerProbe(context.Background(), &live)
	require.NoError(t, err)
	assert.IsType(t, &native.Prober{}, p)
	assert.Contains(t, live.Names(), "ffgo", "a wedged probe restarts the pod (§3.3)")
	require.NotNil(t, live.Get("ffgo"))
	assert.NoError(t, live.Get("ffgo")(nil), "a healthy runtime reports healthy")
}

// TestTheImportDomainRefusesToStartWithoutAProber (W8.6): importing without
// probes would freeze name-derived qualities silently, so a domain that
// cannot probe does not start (spec §6.6), and serves no "ffgo" check.
func TestTheImportDomainRefusesToStartWithoutAProber(t *testing.T) {
	old := newProber
	t.Cleanup(func() { newProber = old })
	newProber = func() (mediainfo.Prober, error) { return nil, ffruntime.ErrUnavailable }
	var live k8s.Checks
	p, err := registerProbe(context.Background(), &live)
	require.ErrorIs(t, err, ffruntime.ErrUnavailable)
	assert.ErrorContains(t, err, "import")
	assert.Nil(t, p)
	assert.Empty(t, live.Names())
}
