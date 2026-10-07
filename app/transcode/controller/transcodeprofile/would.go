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

package transcodeprofile

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// WouldTranscode reports whether a TranscodeProfile will transcode mf: a
// probed file of a kind a profile transcodes, which a profile selects and
// which is not yet transcoded. An audio graft waits for such a transcode to
// ride along with it rather than rewrite the file first. Kept for the
// AudioGraft controller until F7.3 retires it.
func WouldTranscode(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) (bool, error) {
	if !transcodeplan.EligibleKind(mf.Spec.MediaRef.Kind) || !transcodeplan.Probed(mf) || mf.Transcoded() {
		return false, nil
	}
	var l transcodev1alpha1.TranscodeProfileList
	if err := c.List(ctx, &l); err != nil {
		return false, fmt.Errorf("transcodeprofile: list TranscodeProfiles: %w", err)
	}
	winner := transcodeplan.WinningProfile(mf.Labels, l.Items, transcodeplan.DefaultWinner(l.Items))
	return winner != nil && !transcodeplan.AlreadyTranscoded(mf, winner.Name), nil
}
