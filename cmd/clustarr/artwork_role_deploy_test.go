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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	catalogarr "github.com/mediactl/clustarr/app/catalog"
)

// TestBothInstallersRunTheRendererOnTheCatalogarrDeployment holds spec
// §C.6's renderer (catalogarr --role artwork) to a Deployment that runs it:
// a role the binary accepts and no installer starts is inert code that
// passes every other test. Both installers put it on the general catalogarr
// Deployment, which may scale -- the role is not leader-elected and scales
// by consumer -- and never on catalogarr-metadata, the gateway pinned to
// one replica for its in-process rate limiters.
//
// Each Deployment's argv is parsed by the real command tree, so the role
// asserted is the one the pod would run, not a string match on YAML.
func TestBothInstallersRunTheRendererOnTheCatalogarrDeployment(t *testing.T) {
	roleOf := func(t *testing.T, d appsv1.Deployment) catalogarr.Role {
		t.Helper()
		got := stub(t, &runCatalogarr)
		_, err := execute(t, d.Spec.Template.Spec.Containers[0].Args...)
		require.NoError(t, err)
		return got.Role
	}
	check := func(t *testing.T, installer string, byComponent map[string]appsv1.Deployment) {
		t.Helper()
		main, ok := byComponent["catalogarr"]
		require.True(t, ok, "%s renders no catalogarr Deployment", installer)
		gateway, ok := byComponent["catalogarr-metadata"]
		require.True(t, ok, "%s renders no catalogarr-metadata Deployment", installer)

		role := roleOf(t, main)
		require.True(t, role.Valid(), "%s: catalogarr --role %q", installer, role)
		require.True(t, role.Has(catalogarr.RoleArtwork),
			"%s: the catalogarr Deployment runs --role %q, so no pod ever renders an overlay", installer, role)
		require.False(t, role.Has(catalogarr.RoleMetadata),
			"%s: the catalogarr Deployment would start a second metadata gateway", installer)

		gw := roleOf(t, gateway)
		require.False(t, gw.Has(catalogarr.RoleArtwork),
			"%s: the single-replica metadata gateway runs the renderer (--role %q)", installer, gw)
	}

	t.Run("kustomize", func(t *testing.T) {
		paths, err := filepath.Glob("../../config/manager/catalogarr*.yaml")
		require.NoError(t, err)
		byName := map[string]appsv1.Deployment{}
		for _, p := range paths {
			for _, d := range deploymentsIn(t, p) {
				byName[d.Name] = d
			}
		}
		check(t, "config/manager", byName)
	})

	t.Run("chart", func(t *testing.T) {
		helm := findTool(t, "helm")
		root, err := filepath.Abs("../..")
		require.NoError(t, err)
		byComponent := map[string]appsv1.Deployment{}
		for _, d := range decodeRendered(t, run(t, root, helm, "template", "clustarr", "charts/clustarr")).deployments {
			byComponent[d.Spec.Template.Labels["app.kubernetes.io/component"]] = d
		}
		check(t, "charts/clustarr", byComponent)
	})
}
