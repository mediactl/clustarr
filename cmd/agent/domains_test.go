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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/internal/cli/agent"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
)

// TestTheRendererRunsInTheCatalogDomain: the overlay renderer is the catalog
// domain's; artwork fetch is the metadata domain's (R4), so the two writers
// of the artwork object store (one per variant) never share a process.
// Successor to TestBothInstallersRunTheRendererOnTheCatalogarrDeployment's
// code half.
func TestTheRendererRunsInTheCatalogDomain(t *testing.T) {
	homes := map[string][]string{}
	for _, d := range agentdomain.Domains() {
		for _, c := range d.Consumers {
			homes[c] = append(homes[c], d.Name)
		}
	}
	assert.Equal(t, []string{agentdomain.Catalog}, homes[events.ConsumerCatalogArtworkRender])
	assert.Equal(t, []string{agentdomain.Metadata}, homes[events.ConsumerCatalogArtworkFetch])
}

// TestDomainsMatchAgentdomain: every pkg/agentdomain domain but markers (its
// own binary) is a --domain value, and nothing else is: the topology, the
// autoscaler and the installers all read agentdomain.
func TestDomainsMatchAgentdomain(t *testing.T) {
	want := map[string]bool{}
	for _, d := range agentdomain.Domains() {
		if d.Name != agentdomain.Markers {
			want[d.Name] = true
		}
	}
	got := map[string]bool{}
	for _, d := range agent.Domains() {
		assert.False(t, got[d], "%s is listed twice", d)
		got[d] = true
	}
	assert.Equal(t, want, got)
	assert.True(t, got[agentdomain.TorrentEngine] && got[agentdomain.UsenetEngine], "the engines are agents too (§3.5.1)")
}
