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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	livetvmanager "github.com/mediactl/clustarr/app/livetv/manager"
)

// valid is DefaultOptions with the flags an installer always passes.
func valid() Options {
	o := DefaultOptions()
	o.NATSURL, o.NativeImage = "nats://nats:4222", "clustarr/native:dev"
	return o
}

func TestLiveTVDefaults(t *testing.T) {
	o := DefaultOptions()
	assert.Equal(t, ":5004", o.LiveTVBindAddress)
	assert.Equal(t, "47000-47999", o.LiveTVPortRange)
	assert.Equal(t, livetvmanager.DefaultPortRange, o.LiveTVPortRange)
	assert.Equal(t, "livetv", o.LiveTVService)
	require.NoError(t, valid().validateLiveTV())
}

func TestTheLiveTVFlagsAreChecked(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*Options)
		flag   string
	}{
		{"a bind address with no port", func(o *Options) { o.LiveTVBindAddress = "0.0.0.0" }, "--livetv-bind-address"},
		{"port 0", func(o *Options) { o.LiveTVBindAddress = ":0" }, "--livetv-bind-address"},
		{"a range with no dash", func(o *Options) { o.LiveTVPortRange = "47000" }, "--livetv-port-range"},
		{"a range below 1024", func(o *Options) { o.LiveTVPortRange = "80-90" }, "--livetv-port-range"},
		{"a range upside down", func(o *Options) { o.LiveTVPortRange = "48000-47000" }, "--livetv-port-range"},
		{"no Service", func(o *Options) { o.LiveTVService = " " }, "--livetv-service"},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := valid()
			c.mutate(&o)
			err := o.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.flag)
		})
	}
}

func TestLiveTVOffChecksNoLiveTVFlag(t *testing.T) {
	o := valid()
	o.LiveTVBindAddress, o.LiveTVPortRange, o.LiveTVService = "0", "nonsense", ""
	assert.NoError(t, o.validateLiveTV())
}

func TestTheLiveTVFlagsReachTheOptions(t *testing.T) {
	var got Options
	cmd := NewCommandWith(func(_ context.Context, o Options) error { got = o; return nil })
	cmd.SetArgs([]string{
		"--nats-url=nats://nats:4222", "--native-image=clustarr/native:dev", "--leader-election-namespace=clustarr-system",
		"--livetv-bind-address=:5005", "--livetv-port-range=50000-50099", "--livetv-service=tv",
	})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, ":5005", got.LiveTVBindAddress)
	assert.Equal(t, "50000-50099", got.LiveTVPortRange)
	assert.Equal(t, "tv", got.LiveTVService)
}
