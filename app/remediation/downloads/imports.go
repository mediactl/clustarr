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

package downloads

import (
	"context"

	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
)

// importDecisions fills v.Import with importplan's decision for each
// Completed entry (A3.8).
func (s *Stage) importDecisions(_ context.Context, _ owner, _ *lifecycle.View) error { return nil }

// importEffects renders the plan's import dispatches (A3.8).
func (s *Stage) importEffects(_ owner, _ lifecycle.Plan) []remediation.Effect { return nil }

// materialise renders the plan's MediaFile applies and deletes (A3.8).
func (s *Stage) materialise(_ owner, _ lifecycle.Plan) []remediation.Effect { return nil }
