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

package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/segments/decode"
	"github.com/mediactl/clustarr/pkg/segments/textdet"
)

// selfCheckReport is what `markers --self-check` prints.
type selfCheckReport struct {
	FFmpeg      ffruntime.Report `json:"ffmpeg"`
	ONNXRuntime string           `json:"onnxRuntime,omitempty"`
	Failures    []string         `json:"failures,omitempty"`
	OK          bool             `json:"ok"`
}

// selfCheck is `markers --self-check` (spec §3.7, §7.2.9, §10.1.1): FFmpeg 9
// with decode.Needs, and ONNX Runtime from $ORT_LIB_PATH, with no NATS and no
// pod environment. It prints the report as JSON and returns 0 or
// ExitMisconfigured. The detector is required here although markers runs
// without it: the image must carry it.
func selfCheck(w io.Writer, getenv func(string) string) int {
	var r selfCheckReport
	rep, err := ffruntime.Load()
	r.FFmpeg = rep
	if err == nil {
		err = ffruntime.Require(decode.Needs)
	}
	if err != nil {
		r.Failures = append(r.Failures, err.Error())
	}
	ort := getenv("ORT_LIB_PATH")
	if _, err := textdet.NewONNX(ort); err != nil {
		r.Failures = append(r.Failures, fmt.Sprintf("ONNX Runtime ($ORT_LIB_PATH=%q): %v", ort, err))
	} else {
		r.ONNXRuntime = ort
	}
	r.OK = len(r.Failures) == 0
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
	if !r.OK {
		return worker.ExitMisconfigured
	}
	return 0
}
