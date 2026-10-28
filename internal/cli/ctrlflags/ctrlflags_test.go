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

package ctrlflags_test

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli/ctrlflags"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func names(fs *pflag.FlagSet) map[string]bool {
	out := map[string]bool{}
	fs.VisitAll(func(f *pflag.Flag) { out[f.Name] = true })
	return out
}

// The eleven flags every controller manifest wrote, with --leader-elect on by
// default (spec §3.4.1).
func TestBindManagerBindsElevenFlagsAndElectsByDefault(t *testing.T) {
	fs := pflag.NewFlagSet("manager", pflag.ContinueOnError)
	o := ctrlflags.BindManager(fs)
	require.NoError(t, fs.Parse(nil))
	got := names(fs)
	assert.Len(t, got, 11)
	for _, name := range []string{"metrics-bind-address", "metrics-secure", "health-probe-bind-address",
		"pprof-bind-address", "leader-elect", "leader-election-namespace", "namespace", "watch-namespace",
		"nats-url", "nats-single-node", "graceful-shutdown-timeout"} {
		assert.True(t, got[name], name)
	}
	assert.True(t, o.LeaderElect, "every controller manifest passed --leader-elect; the manager defaults it on")
	assert.False(t, o.BusSingleNode)
	require.NoError(t, fs.Parse([]string{"--leader-elect=false", "--nats-single-node",
		"--leader-election-namespace=leases", "--watch-namespace=a,b"}))
	assert.False(t, o.LeaderElect)
	assert.True(t, o.BusSingleNode)
	assert.Equal(t, "leases", o.LeaderElectionNamespace)
	assert.Equal(t, []string{"a", "b"}, o.WatchNamespaces)
}

// An agent never elects and never ensures the topology (spec §3.5.1): the
// three flags are not there to set.
func TestBindAgentHasNoElectionOrTopologyFlags(t *testing.T) {
	fs := pflag.NewFlagSet("agent", pflag.ContinueOnError)
	o := ctrlflags.BindAgent(fs)
	got := names(fs)
	assert.Len(t, got, 8)
	for _, retired := range []string{"leader-elect", "leader-election-namespace", "nats-single-node"} {
		assert.False(t, got[retired], retired)
		assert.Error(t, fs.Parse([]string{"--" + retired}), "%s is refused by an agent", retired)
	}
	require.NoError(t, fs.Parse(nil))
	assert.False(t, o.LeaderElect)
	assert.False(t, o.BusSingleNode)
}

func TestNATSURLAndNamespaceComeFromTheEnvironment(t *testing.T) {
	t.Setenv("NATS_URL", "nats://nats.clustarr-system.svc:4222")
	t.Setenv("POD_NAMESPACE", "media")
	for name, bind := range map[string]func(*pflag.FlagSet) *k8s.Options{
		"manager": ctrlflags.BindManager, "agent": ctrlflags.BindAgent,
	} {
		fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
		o := bind(fs)
		require.NoError(t, fs.Parse(nil))
		assert.Equal(t, "nats://nats.clustarr-system.svc:4222", o.NATSURL, name)
		assert.Equal(t, "media", o.Namespace, name)

		// A flag still wins over the environment.
		fs = pflag.NewFlagSet(name, pflag.ContinueOnError)
		o = bind(fs)
		require.NoError(t, fs.Parse([]string{"--nats-url=nats://other:4222", "--namespace=elsewhere"}))
		assert.Equal(t, "nats://other:4222", o.NATSURL, name)
		assert.Equal(t, "elsewhere", o.Namespace, name)
	}
}

func TestParseGIDs(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    []int64
		wantErr bool
	}{
		{in: "", want: nil}, {in: "  ", want: nil}, {in: " 109 ", want: []int64{109}},
		{in: "44,109", want: []int64{44, 109}}, {in: "44, 109", want: []int64{44, 109}}, {in: "0", want: []int64{0}},
		{in: "-1", wantErr: true}, {in: "44,,109", wantErr: true}, {in: "44,", wantErr: true},
		{in: "video", wantErr: true}, {in: "44;109", wantErr: true},
	} {
		got, err := ctrlflags.ParseGIDs(tc.in)
		if tc.wantErr {
			assert.Error(t, err, tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}

// One test binary runs Execute many times; a second registration must not
// fail as prometheus.AlreadyRegisteredError.
func TestRegisterMetricsIsIdempotent(t *testing.T) {
	require.NoError(t, ctrlflags.RegisterMetrics())
	require.NoError(t, ctrlflags.RegisterMetrics())
}

// The manager serves net/http/pprof on loopback unless told otherwise (owner,
// 2026-10-28: a remediation backlog nobody could profile), reached through
// kubectl port-forward as the usenet engine's is; an empty flag turns it off,
// and an agent serves none by default.
func TestTheManagerServesPprofOnLoopbackByDefault(t *testing.T) {
	fs := pflag.NewFlagSet("manager", pflag.ContinueOnError)
	o := ctrlflags.BindManager(fs)
	require.NoError(t, fs.Parse(nil))
	assert.Equal(t, "127.0.0.1:6060", o.PprofBindAddress)

	fs = pflag.NewFlagSet("manager", pflag.ContinueOnError)
	o = ctrlflags.BindManager(fs)
	require.NoError(t, fs.Parse([]string{"--pprof-bind-address="}))
	assert.Empty(t, o.PprofBindAddress)

	fs = pflag.NewFlagSet("agent", pflag.ContinueOnError)
	o = ctrlflags.BindAgent(fs)
	require.NoError(t, fs.Parse(nil))
	assert.Empty(t, o.PprofBindAddress)
}
