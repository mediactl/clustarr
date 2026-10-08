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

package remediation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestPlannerOrder holds §3.5's order: the probe first (every later planner
// reads its draft), graft after transcode (the join is decided at dispatch),
// naming after both (it holds a rename while either is in flight), subtitles
// after naming (sidecars follow the stem), markers last (it gates on the
// draft's probe hash).
func TestPlannerOrder(t *testing.T) {
	assert.Equal(t, []remediation.PlannerName{"probe", "transcode", "graft", "naming", "subtitles", "markers"}, remediation.Order)
}

// NewReconciler runs bound planners in Order, whatever order they are given
// in, and refuses a name Order does not know or a name given twice.
func TestNewReconcilerSortsPlannersAndRefusesStrangers(t *testing.T) {
	markers := remediationtest.MarkersFake(nil)
	naming := remediationtest.NamingFake(nil)
	probe := remediationtest.ProbeFake(nil)
	r, err := remediation.NewReconciler(nil, &remediation.Env{}, nil, remediation.Config{},
		[]remediation.Bound{remediation.Bind[int](markers), remediation.Bind[int](naming), remediation.Bind[int](probe)})
	require.NoError(t, err)
	assert.Equal(t, []remediation.PlannerName{"probe", "naming", "markers"}, r.PlannerNames())

	_, err = remediation.NewReconciler(nil, &remediation.Env{}, nil, remediation.Config{},
		[]remediation.Bound{remediation.Bind[int](naming), remediation.Bind[int](remediationtest.NamingFake(nil))})
	require.ErrorContains(t, err, `planner "naming" is bound twice`)

	stranger := remediationtest.NamingFake(nil)
	stranger.N = "adoptx"
	_, err = remediation.NewReconciler(nil, &remediation.Env{}, nil, remediation.Config{},
		[]remediation.Bound{remediation.Bind[int](stranger)})
	require.ErrorContains(t, err, `planner "adoptx" is not in remediation.Order`)
}

func TestFileKeyOfARef(t *testing.T) {
	k, ok := remediation.FileKey(schema.Ref{Namespace: "media", Name: "heat-abc", UID: "u"})
	require.True(t, ok)
	assert.Equal(t, remediation.Key{Kind: remediation.KindMediaFile, Namespace: "media", Name: "heat-abc"}, k)
	_, ok = remediation.FileKey(schema.Ref{Namespace: "media", UID: "u"})
	assert.False(t, ok, "a ref with no name names no file")
}
