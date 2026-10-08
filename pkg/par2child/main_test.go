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

package par2child_test

import (
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	par2 "github.com/mediactl/par2go"

	"github.com/mediactl/clustarr/pkg/par2child"
)

// TestMain makes this test binary the child: "<bin> par2-repair ..." is the
// real one (ChildMain), "<bin> par2-fake <mode>" a fake that needs no
// library.
func TestMain(m *testing.M) {
	par2child.RunIfChild()
	if len(os.Args) > 2 && os.Args[1] == "par2-fake" {
		os.Exit(fakeChild(os.Args[2]))
	}
	os.Exit(m.Run())
}

// fakeChild speaks the child's protocol (§8.4.3) -- a framed gob Request on
// stdin, a gob Result on fd 3 -- and behaves as mode says.
func fakeChild(mode string) int {
	var n uint32
	if err := binary.Read(os.Stdin, binary.BigEndian, &n); err != nil {
		return 3
	}
	var req par2child.Request
	if err := gob.NewDecoder(io.LimitReader(os.Stdin, int64(n))).Decode(&req); err != nil {
		return 3
	}
	out := os.NewFile(3, "par2-result")
	write := func(r par2child.Result) int { _ = gob.NewEncoder(out).Encode(r); return 0 }
	switch mode {
	case "echo":
		return write(par2child.Result{Status: par2.Repaired, BlocksNeeded: 7, Log: req.Index + "|" + strings.Join(req.Extras, "|")})
	case "cooperative":
		_, _ = io.Copy(io.Discard, os.Stdin) // the parent's cancel closes stdin
		return write(par2child.Result{ErrKind: par2child.ErrKindCancelled, Err: "cancelled"})
	case "stubborn":
		time.Sleep(time.Minute)
		return 0
	case "die":
		fmt.Fprintln(os.Stderr, "boom: the child crashed")
		return 2
	case "env":
		return write(par2child.Result{Log: strings.Join(os.Environ(), "\n")})
	}
	return 3
}

// fake is an Exec that runs this test binary as the fake child in mode, with
// an empty environment and a short grace.
func fake(mode string) par2child.Exec {
	return par2child.Exec{Path: os.Args[0], Args: []string{"par2-fake", mode}, Env: []string{}, Grace: 300 * time.Millisecond}
}
