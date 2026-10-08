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

package k8s_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestSanitizeTextReplacesWhatTheApplyDecoderRefuses: the apply-patch+yaml
// reader refuses U+0080-U+009F except U+0085, and U+FFFE and U+FFFF;
// encoding/json leaves them raw, so one of them refuses the whole apply
// (loop spec §2.11.1 rule 3: one live MediaFile failed every status apply
// this way about 346 times in 26.5 h).
func TestSanitizeTextReplacesWhatTheApplyDecoderRefuses(t *testing.T) {
	for r := rune(0x80); r <= 0x9F; r++ {
		assert.Equal(t, r != 0x85, k8s.Unapplyable(r), "U+%04X", r)
	}
	assert.True(t, k8s.Unapplyable(0xFFFE))
	assert.True(t, k8s.Unapplyable(0xFFFF))
	for _, r := range []rune{'\t', '\n', 'a', 0x7F, 0x85, 0xA0, 0xFEFF, 0xFFFD, 0x1F600} {
		assert.False(t, k8s.Unapplyable(r), "U+%04X", r)
	}
	assert.Equal(t, "a�b", k8s.SanitizeText("a\u0096b"))
	assert.Equal(t, "a�b", k8s.SanitizeText("a\xffb"), "an invalid byte becomes U+FFFD, as encoding/json makes it")
	plain := "Commentary – Director"
	assert.Equal(t, plain, k8s.SanitizeText(plain))
	assert.False(t, k8s.HasUnapplyable(k8s.SanitizeText("\u0080\u009f￿")))
	assert.True(t, utf8.ValidString(k8s.SanitizeText("\u0080\xfe￾")))
	assert.True(t, k8s.HasUnapplyable("x\u0096"))
	assert.False(t, k8s.HasUnapplyable(plain))
}

func TestClampTextNeverSplitsARune(t *testing.T) {
	assert.Equal(t, "abc", k8s.ClampText("abc", 5))
	assert.Equal(t, "abc", k8s.ClampText("abc", 3))
	assert.Equal(t, "ab", k8s.ClampText("abc", 2))
	assert.Equal(t, "é", k8s.ClampText("éé", 3), "a rune that does not fit is left out whole")
	clamped := k8s.ClampText(strings.Repeat("😀", 10), 7)
	assert.True(t, utf8.ValidString(clamped))
	assert.LessOrEqual(t, len(clamped), 7)
	assert.Equal(t, "😀", clamped, "one whole four-byte rune fits in seven bytes, two do not")
	assert.Empty(t, k8s.ClampText("😀", 3))
	assert.Equal(t, 1024, k8s.MaxConditionMessage)
}
