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

package providerset

import subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"

// capability is what a SubtitleProviderType's client declares about
// itself that the manager reports without building one (spec §13 OD9).
type capability struct {
	needsSecrets []string
	hiVerifiable bool
}

// capabilities has one row per type with a client; whisper has none.
// Each row restates its client's Capabilities().NeedsSecrets and
// HIVerifiable(); build's TestCapabilityTableMatchesTheClients fails if
// one drifts or a type the CRD admits gains a client without a row.
var capabilities = map[subtitlev1alpha1.SubtitleProviderType]capability{
	subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom: {
		needsSecrets: []string{
			subtitlev1alpha1.ProviderSecretKeyAPIKey,
			subtitlev1alpha1.ProviderSecretKeyUsername,
			subtitlev1alpha1.ProviderSecretKeyPassword,
		},
		hiVerifiable: true,
	},
	subtitlev1alpha1.SubtitleProviderGestdown:  {hiVerifiable: true},
	subtitlev1alpha1.SubtitleProviderSubDL:     {needsSecrets: []string{subtitlev1alpha1.ProviderSecretKeyAPIKey}, hiVerifiable: true},
	subtitlev1alpha1.SubtitleProviderSubSource: {needsSecrets: []string{subtitlev1alpha1.ProviderSecretKeyAPIKey}, hiVerifiable: true},
	subtitlev1alpha1.SubtitleProviderEmbedded:  {hiVerifiable: true},
}
