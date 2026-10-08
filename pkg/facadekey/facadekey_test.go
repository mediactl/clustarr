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

package facadekey_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/pkg/facadekey"
)

// TestKeysOfIsEveryNonBlankEntryInKeyOrder: every non-blank value counts,
// whatever its data key, trimmed and in key order, so an operator rotates
// by adding an entry; no Secret is no key.
func TestKeysOfIsEveryNonBlankEntryInKeyOrder(t *testing.T) {
	assert.Nil(t, facadekey.KeysOf(nil))
	sec := &corev1.Secret{Data: map[string][]byte{
		facadekey.APIKeyField: []byte(" generated\n"), "rotated": []byte("next"), "emptied": []byte("  "),
	}}
	assert.Equal(t, []string{"generated", "next"}, facadekey.KeysOf(sec))
}
