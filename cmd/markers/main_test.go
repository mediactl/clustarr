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

	"github.com/mediactl/clustarr/app/segments/worker"
)

func TestMissingNATSURLExitsMisconfigured(t *testing.T) {
	env := map[string]string{"POD_NAME": "segmentarr-worker-0"}
	assert.Equal(t, worker.ExitMisconfigured, run(nil, func(k string) string { return env[k] }))
}

func TestABadFlagExitsMisconfigured(t *testing.T) {
	assert.Equal(t, worker.ExitMisconfigured, run([]string{"--no-such-flag"}, func(string) string { return "" }))
}
