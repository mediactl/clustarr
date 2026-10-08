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

package par2child

import (
	"context"
	"fmt"

	par2 "github.com/mediactl/par2go"
)

// InProcess runs par2go in this process: the child itself, and tests. The
// engine never uses it (see the package doc).
type InProcess struct{}

var _ Runner = InProcess{}

// Run repairs req's set. A done ctx returns the result and an error wrapping
// ctx.Err(); otherwise the error is the result's own ([Result.Error]).
func (InProcess) Run(ctx context.Context, req Request) (Result, error) {
	res, err := par2.Repair(ctx, req.Index, par2.Options{
		Dir:         req.Dir,
		ExtraFiles:  req.Extras,
		MemoryLimit: req.MemoryLimit,
		Threads:     req.Threads,
		FileThreads: req.FileThreads,
	})
	out := Result{
		Status:       res.Status,
		BlocksNeeded: res.BlocksNeeded,
		Headers:      res.Headers,
		Log:          res.Log,
		ErrKind:      kindOf(err),
		Library:      Library(),
	}
	if err != nil {
		out.Err = err.Error()
	}
	if ctx.Err() != nil {
		return out, fmt.Errorf("par2child: run interrupted: %w", ctx.Err())
	}
	return out, out.Error()
}

// Probe loads the library.
func (InProcess) Probe(context.Context) error { return par2.Available() }
