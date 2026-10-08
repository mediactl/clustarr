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

package depguard_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/guards/depguard"
)

const ffgo = "github.com/obinnaokechukwu/ffgo"

// The guards are only as good as the helper; prove it finds a known chain,
// and that each check fails where it must.
func TestTheHelperFindsAKnownChain(t *testing.T) {
	g := depguard.Load(t, "github.com/mediactl/clustarr/cmd/transcode", "CGO_ENABLED=0")
	require.True(t, g.Has(ffgo))
	chain := g.Chain(ffgo)
	require.NotEmpty(t, chain)
	assert.Equal(t, "github.com/mediactl/clustarr/cmd/transcode", chain[0])
	assert.Equal(t, ffgo, chain[len(chain)-1])
	for i := 1; i < len(chain); i++ {
		assert.Contains(t, g.Imports(chain[i-1]), chain[i], "each link of the chain is a real import")
	}
	assert.Nil(t, g.Chain("github.com/mediactl/clustarr/no/such/package"))

	rec := &recorder{TB: t}
	g.Deny(rec, []depguard.Rule{{Path: ffgo, Why: "probe"}})
	assert.True(t, rec.failed, "Deny must fail on a linked package")
	rec = &recorder{TB: t}
	g.Deny(rec, []depguard.Rule{{Path: "github.com/mediactl/clustarr/no/such/package", Why: "probe"}})
	assert.False(t, rec.failed, "Deny passes a package the closure lacks")
	rec = &recorder{TB: t}
	g.Require(rec, "github.com/mediactl/clustarr/no/such/package", "probe")
	assert.True(t, rec.failed, "Require must fail on a missing anchor")
	rec = &recorder{TB: t}
	g.AllowApp(rec, nil)
	assert.True(t, rec.failed, "AllowApp with nothing allowed must fail: transcode links app/ packages")
	rec = &recorder{TB: t}
	g.OnlyImportedBy(rec, func(p string) bool { return p == ffgo }, func(string) bool { return false }, "probe")
	assert.True(t, rec.failed, "OnlyImportedBy must fail when no importer is allowed")
}

// recorder captures failures instead of failing the test.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Errorf(string, ...any) { r.failed = true }
func (r *recorder) Helper()               {}

func TestRuleMatching(t *testing.T) {
	assert.True(t, depguard.Rule{Path: "a/b"}.Matches("a/b"))
	assert.True(t, depguard.Rule{Path: "a/b"}.Matches("a/b/c"))
	assert.False(t, depguard.Rule{Path: "a/b"}.Matches("a/bc"))
	assert.False(t, depguard.Rule{Path: "a/b", Exact: true}.Matches("a/b/c"))
	assert.True(t, depguard.Rule{Path: "a/b", Exact: true}.Matches("a/b"))
	assert.False(t, depguard.Rule{Path: "a/b", Except: []string{"a/b/ok"}}.Matches("a/b/ok"))
	assert.True(t, depguard.Rule{Path: "a/b", Except: []string{"a/b/ok"}}.Matches("a/b/ok/deeper"))
	assert.True(t, depguard.Rule{Regexp: regexp.MustCompile(`^x/app/[a-z]+/agent(/|$)`)}.Matches("x/app/catalog/agent/events"))
	assert.False(t, depguard.Rule{Regexp: regexp.MustCompile(`^x/app/[a-z]+/agent(/|$)`)}.Matches("x/app/catalog/agents"))
}
