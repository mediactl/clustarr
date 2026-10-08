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
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	par2 "github.com/mediactl/par2go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli/agent"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/test/installtest"
)

// parse runs the agent's real command tree with a recording RunFunc and
// returns the options the command line produced. POD_NAMESPACE is what the
// downward API sets; the index domain's facade needs it. It is read when
// the command is built, so a test sets the environment before calling parse.
func parse(t *testing.T, args ...string) (agent.Options, error) {
	t.Helper()
	if _, ok := os.LookupEnv("POD_NAMESPACE"); !ok {
		t.Setenv("POD_NAMESPACE", "default")
	}
	var got agent.Options
	cmd := agent.NewCommandWith(func(_ context.Context, o agent.Options) error { got = o; return nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return got, cmd.Execute()
}

// oneDomain is the refusal of anything but exactly one known --domain.
const oneDomain = "--domain takes exactly one of catalog, events, metadata, import, index, caption, torrent-engine, usenet-engine"

// TestUnknownDomainIsRejected: one process runs exactly one domain (§3.5.1);
// there is no "all" and no list.
func TestUnknownDomainIsRejected(t *testing.T) {
	for _, args := range [][]string{nil, {"--domain="}, {"--domain=all"}, {"--domain=catalog,events"}, {"--domain=nope"}, {"--domain=markers"}} {
		_, err := parse(t, args...)
		assert.EqualError(t, err, oneDomain, "%v", args)
	}
}

// TestEveryDomainIsAccepted: every --domain value parses with only the
// flags it cannot start without, and reaches the options as given.
func TestEveryDomainIsAccepted(t *testing.T) {
	extra := map[string][]string{
		"torrent-engine": {"--engine=sab-0"},
		"usenet-engine":  {"--engine=nzb-0", "--scratch-dir=/scratch"},
	}
	for _, d := range agent.Domains() {
		o, err := parse(t, append([]string{"--domain=" + d}, extra[d]...)...)
		require.NoError(t, err, d)
		assert.Equal(t, d, o.Domain)
	}
}

// TestDomainFlagsNeedTheirDomain: a domain flag set for a domain that does
// not read it is a mistake, not a no-op (§3.5.1). An environment default is
// not "set": CLUSTARR_INDEX_PATH, which the index Deployment sets, is fine
// on any domain.
func TestDomainFlagsNeedTheirDomain(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--domain=catalog", "--engine=x-0"}, "--engine is only meaningful with --domain torrent-engine or usenet-engine"},
		{[]string{"--domain=import", "--index-path=/x"}, "--index-path is only meaningful with --domain index"},
		{[]string{"--domain=catalog", "--data-dir=/data"}, "--data-dir is only meaningful with --domain import, caption, torrent-engine or usenet-engine"},
		{[]string{"--domain=torrent-engine", "--engine=sab-0", "--publish-dir=/data/x"}, "--publish-dir is only meaningful with --domain usenet-engine"},
		{[]string{"--domain=caption", "--sample-max-bytes=0"}, "--sample-max-bytes is only meaningful with --domain import"},
		{[]string{"--domain=index", "--download-client=sab"}, "--download-client is only meaningful with --domain torrent-engine"},
	} {
		_, err := parse(t, tc.args...)
		assert.EqualError(t, err, tc.want, "%v", tc.args)
	}
	t.Setenv("CLUSTARR_INDEX_PATH", "/var/lib/clustarr/index/releases.db")
	_, err := parse(t, "--domain=catalog")
	require.NoError(t, err)
}

// TestEngineDomainsNeedAnIdentity: an engine serves one "<client>-<ordinal>"
// identity, given as --engine or, for a torrent StatefulSet replica, derived
// from --download-client and $POD_NAME (§3.5.5).
func TestEngineDomainsNeedAnIdentity(t *testing.T) {
	_, err := parse(t, "--domain=torrent-engine")
	require.ErrorContains(t, err, "exactly one of --engine or --download-client")
	_, err = parse(t, "--domain=torrent-engine", "--engine=sab-0", "--download-client=sab")
	require.ErrorContains(t, err, "exactly one of --engine or --download-client")

	t.Setenv("POD_NAME", "sab-engine-2")
	o, err := parse(t, "--domain=torrent-engine", "--download-client=sab")
	require.NoError(t, err)
	assert.Equal(t, "sab-2", o.Engine, "the ordinal comes from POD_NAME (§3.5.5)")

	t.Setenv("POD_NAME", "sab-7f9c")
	_, err = parse(t, "--domain=torrent-engine", "--download-client=sab")
	require.ErrorContains(t, err, "is not a replica")

	_, err = parse(t, "--domain=usenet-engine", "--scratch-dir=/scratch")
	require.ErrorContains(t, err, "--domain usenet-engine needs --engine")
	_, err = parse(t, "--domain=usenet-engine", "--engine=nzb-0", "--scratch-dir=")
	require.ErrorContains(t, err, "--scratch-dir")
}

// TestAgentManagersNeverElect: an agent has no election or topology flag,
// and its manager options take no lease (R9).
func TestAgentManagersNeverElect(t *testing.T) {
	for _, flag := range []string{"--leader-elect", "--leader-election-namespace=x", "--nats-single-node"} {
		_, err := parse(t, "--domain=catalog", flag)
		assert.ErrorContains(t, err, "unknown flag", flag)
	}
	o, err := parse(t, "--domain=index")
	require.NoError(t, err)
	opts := o.Options.AgentManagerOptions()
	assert.False(t, opts.LeaderElection)
	require.NotNil(t, opts.Controller.NeedLeaderElection)
	assert.False(t, *opts.Controller.NeedLeaderElection, "an agent's controllers are declared non-leader")
}

// TestRetiredFlagsAreGone: the per-service binaries' role, image and claim
// flags belong to no agent (the manager renders the engines' pods).
func TestRetiredFlagsAreGone(t *testing.T) {
	for _, flag := range []string{"--role=worker", "--cardigann-bundled=false", "--engine-image=x",
		"--data-claim=x", "--engine-service-account=x", "--data-path=/data"} {
		_, err := parse(t, "--domain=import", flag)
		assert.ErrorContains(t, err, "unknown flag", flag)
	}
}

// requireNative skips without FFmpeg 9 and par2go, and fails instead under
// installtest.RequireTools (make test's NATIVE_ENV).
func requireNative(t *testing.T) {
	t.Helper()
	for what, err := range map[string]error{"FFmpeg 9": ffruntime.Require(ffruntime.Needs{}), "par2go": par2.Available()} {
		if err == nil {
			continue
		}
		if installtest.RequireTools() {
			t.Fatalf("%s: %v", what, err)
		}
		t.Skipf("%s: %v", what, err)
	}
}

// TestSelfCheckNeedsNoDomain: the image check runs `agent --self-check` with
// no domain, no cluster and no NATS (§10.1.1), and it proves what the
// domains load: the cgo piece completion (R13), FFmpeg 9 with the import and
// caption needs, and par2go for the usenet engine's repair child.
func TestSelfCheckNeedsNoDomain(t *testing.T) {
	requireNative(t)
	var out bytes.Buffer
	cmd := agent.NewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--self-check"})
	require.NoError(t, cmd.Execute(), "%s", out.String())
	for _, want := range []string{`"pieceCompletion": "sqlite"`, `"ffmpegMajor": 9`, `"par2go": "`, "libpar2shim", `"ok": true`} {
		assert.Contains(t, out.String(), want)
	}
}

// TestASelfCheckReportsAMissingDecoder: a self-check fails naming each
// FFmpeg piece a domain needs and the build lacks, in its report as well as
// its error.
func TestASelfCheckReportsAMissingDecoder(t *testing.T) {
	requireNative(t)
	var out bytes.Buffer
	err := agent.SelfCheckWith(&out, ffruntime.Needs{Decoders: []string{"no-such-decoder"}})
	require.ErrorContains(t, err, "decoder no-such-decoder")
	assert.Contains(t, out.String(), "decoder no-such-decoder")
	assert.Contains(t, out.String(), `"ok": false`)
}
