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

package transcodejob

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestIsPoolDurable holds the sweep's notion of a pool durable to the one
// builder of pool durable names, and keeps every durable of the shipped
// topology -- squasharr-transcode-results above all, which shares the
// prefix -- out of it (final-review M1).
func TestIsPoolDurable(t *testing.T) {
	for _, class := range poolClasses {
		for _, uid := range []string{"8b2c1d9e-0f4a-4c1b-9d2e-3f4a5b6c7d8e", "a.b c"} {
			name := events.TranscodeTaskConsumerName(uid, string(class))
			assert.True(t, isPoolDurable(name), "%s is a pool durable", name)
		}
	}
	for _, c := range events.Default().Consumers {
		assert.False(t, isPoolDurable(c.Name), "%s is the shipped topology's, never swept", c.Name)
	}
	assert.False(t, isPoolDurable("squasharr-transcode-results"))
	assert.False(t, isPoolDurable("importarr-scan"))
}
