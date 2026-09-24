/*
Copyright 2026 The clustarr Authors.

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

package main

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestBothInstallersRaiseTheNATSMaxPayload holds the chart's NATS
// configuration to the limit config/nats/configmap.yaml has always set
// (design spec §12): max_payload 8Mi. The nats subchart's default is the
// server's 1 MiB, under which indexarr's rpc.indexarr.download reply -- the
// .nzb inline, base64 in JSON -- is refused for any 1080p movie's ~1.3 MB
// .nzb, and the grab sits in Assigned forever (2026-09-24, the owner's
// cluster, Helm release revision 36). The two installers render the value
// differently (the subchart emits JSON, kustomize a conf block), so this
// matches each in its own spelling rather than comparing documents.
func TestBothInstallersRaiseTheNATSMaxPayload(t *testing.T) {
	helm := findTool(t, "helm")
	kustomize := findTool(t, "kustomize")

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}

	chart := run(t, root, helm, "template", "clustarr", "charts/clustarr")
	if want := regexp.MustCompile(`"max_payload":\s*8Mi\b`); !want.Match(chart) {
		t.Errorf("helm template charts/clustarr renders no NATS max_payload of 8Mi; "+
			"nats.config.merge.max_payload in charts/clustarr/values.yaml must be %q", "<< 8Mi >>")
	}

	kz := run(t, root, kustomize, "build", "config/default")
	if want := regexp.MustCompile(`(?m)^\s*max_payload:\s*8Mi\b`); !want.Match(kz) {
		t.Errorf("kustomize build config/default renders no NATS max_payload of 8Mi; " +
			"config/nats/configmap.yaml must keep it")
	}
}
