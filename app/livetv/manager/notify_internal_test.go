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

package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestATunerCountNoticeReachesOnlyTheProviderController(t *testing.T) {
	providers, channels := make(chan event.GenericEvent, 4), make(chan event.GenericEvent, 4)
	notify, notifyTuners := notifiers(providers, channels)
	key := types.NamespacedName{Namespace: "media", Name: "news"}

	notify(key)
	assert.Len(t, providers, 1)
	assert.Len(t, channels, 1, "a snapshot change wakes the channels")
	<-providers
	<-channels

	notifyTuners(key)
	assert.Len(t, providers, 1, "the provider's status shows the tuners in use")
	assert.Empty(t, channels, "no channel's status does")
}
