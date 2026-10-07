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
)

// selfCheckReport is `agent --self-check`'s JSON report.
type selfCheckReport struct {
	PieceCompletion string `json:"pieceCompletion"`
	OK              bool   `json:"ok"`
}

// runSelfCheck is `agent --self-check` (§10.1.1): no domain, no cluster, no
// NATS. A bolt piece completion means a cgo-free build, under which every
// seeding torrent would rehash (R13). The ffgo and par2go checks join it
// when the agent links them (spec §11.1 steps 11 and 12).
func runSelfCheck(w io.Writer) error {
	r := selfCheckReport{PieceCompletion: pieceCompletion, OK: pieceCompletion == "sqlite"}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("self-check: the torrent piece completion is %q, not sqlite: this agent was built without cgo (R13)", r.PieceCompletion)
	}
	return nil
}
