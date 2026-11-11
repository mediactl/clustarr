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

package version

import "testing"

// ImageTag is the tag of an image reference, the value the pods the
// manager builds carry as app.kubernetes.io/version: a registry's port is
// not a tag, a digest is not a tag (its colon would not be a label value),
// and a bare reference has none.
func TestImageTag(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/mediactl/clustarr/native:sha-abc123": "sha-abc123",
		"native:0.1.0":                                            "0.1.0",
		"registry:5000/mediactl/native":                           "",
		"registry:5000/mediactl/native:v1":                        "v1",
		"ghcr.io/mediactl/native@sha256:0123456789abcdef":         "",
		"ghcr.io/mediactl/native:sha-abc@sha256:0123456789abcdef": "sha-abc",
		"native": "",
		"":       "",
	} {
		if got := ImageTag(ref); got != want {
			t.Errorf("ImageTag(%q) = %q, want %q", ref, got, want)
		}
	}
}
