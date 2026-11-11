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

package tracing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/version"
)

// The spans' resource carries the identity every log record does (the
// 2026-11-10 logging design §3), so a trace and its lines share one
// service.name, service.namespace and service.version.
func TestTheTracerResourceNamesTheNamespaceAndVersion(t *testing.T) {
	attrs := map[string]string{}
	for _, kv := range newResource("manager").Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	require.Equal(t, "manager", attrs["service.name"])
	require.Equal(t, "clustarr", attrs["service.namespace"])
	require.Equal(t, version.Version, attrs["service.version"])
}
