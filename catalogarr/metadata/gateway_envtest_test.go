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

package metadata_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/catalogarr/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

func TestSetupBuildsAndStartsTheGatewayWithNoProvidersConfigured(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	bus := membus.New(clockwork.NewRealClock())
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))

	stop, err := metadata.Setup(ctx, metadata.Options{Client: c, Bus: bus, HTTPClient: http.DefaultClient})
	require.NoError(t, err)
	require.NotNil(t, stop)
	stop()
}

func TestSetupRejectsMissingRequiredOptions(t *testing.T) {
	_, err := metadata.Setup(context.Background(), metadata.Options{})
	require.Error(t, err)
}
