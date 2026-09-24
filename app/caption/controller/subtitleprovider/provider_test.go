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

package subtitleprovider

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestJudge maps every verdict providerset.Validate can return onto the
// conditions this controller reports. The checks themselves are
// providerset's, and TestValidateAndEntryAgree there holds them to the
// fetch worker's builder.
func TestJudge(t *testing.T) {
	missing := fmt.Errorf("%w: secret media/creds has no [password]", providerset.ErrMissingSecret)
	for _, tc := range []struct {
		name          string
		typ           subtitlev1alpha1.SubtitleProviderType
		err           error
		implemented   bool
		authenticated bool
		reason        string
		message       string
	}{
		{
			"no client (whisper, spec-deferred)", subtitlev1alpha1.SubtitleProviderWhisper, fmt.Errorf("%w: whisper", providerset.ErrNoClient),
			false, false, ReasonNotImplemented, `no client for provider type "whisper"`,
		},
		{
			"subdl with its API key", subtitlev1alpha1.SubtitleProviderSubDL, nil,
			true, true, ReasonCredentialsPresent, "subdl",
		},
		{
			"missing credentials", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, missing,
			true, false, k8s.ReasonDependencyNotReady, "password",
		},
		{
			"credentials present", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, nil,
			true, true, ReasonCredentialsPresent, "opensubtitlescom",
		},
		{
			"gestdown needs none", subtitlev1alpha1.SubtitleProviderGestdown, nil,
			true, true, ReasonNoCredentialsRequired, "needs no credentials",
		},
		{
			"embedded needs none", subtitlev1alpha1.SubtitleProviderEmbedded, nil,
			true, true, ReasonNoCredentialsRequired, "needs no credentials",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := judge(tc.typ, tc.err)
			assert.Equal(t, tc.implemented, got.implemented, "implemented")
			assert.Equal(t, tc.authenticated, got.authenticated, "authenticated")
			assert.Equal(t, tc.reason, got.reason)
			assert.Contains(t, got.message, tc.message)
		})
	}
}
