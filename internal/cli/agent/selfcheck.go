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

package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	mediainfonative "github.com/mediactl/clustarr/pkg/mediainfo/native"
	embeddednative "github.com/mediactl/clustarr/pkg/subtitles/providers/embedded/native"
)

// selfCheckReport is `agent --self-check`'s JSON report.
type selfCheckReport struct {
	PieceCompletion string            `json:"pieceCompletion"`
	FFmpeg          *ffruntime.Report `json:"ffmpeg,omitempty"`
	Failures        []string          `json:"failures,omitempty"`
	OK              bool              `json:"ok"`
}

// runSelfCheck is `agent --self-check` (§10.1.1): the cgo piece completion
// (R13) and FFmpeg 9 with what the import and caption domains need, with no
// domain, cluster or NATS. A bolt piece completion means a cgo-free build,
// under which every seeding torrent would rehash. W9.6 adds par2go.
func runSelfCheck(w io.Writer) error {
	return SelfCheckWith(w, mediainfonative.Needs, embeddednative.Needs)
}

// SelfCheckWith is runSelfCheck with the FFmpeg needs given, for tests.
func SelfCheckWith(w io.Writer, needs ...ffruntime.Needs) error {
	r := selfCheckReport{PieceCompletion: pieceCompletion}
	if pieceCompletion != "sqlite" {
		r.Failures = append(r.Failures, fmt.Sprintf("the torrent piece completion is %q, not sqlite: this agent was built without cgo (R13)", pieceCompletion))
	}
	rep, err := ffruntime.Load()
	r.FFmpeg = &rep
	for _, n := range needs {
		if err == nil {
			err = ffruntime.Require(n)
		}
	}
	if err != nil {
		r.Failures = append(r.Failures, err.Error())
	}
	r.OK = len(r.Failures) == 0
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("self-check: %s", strings.Join(r.Failures, "; "))
	}
	return nil
}
