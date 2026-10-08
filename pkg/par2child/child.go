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
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"os"

	par2 "github.com/mediactl/par2go"
)

// ChildMain is "agent par2-repair". It reads one gob [Request] (a big-endian
// uint32 length first) from stdin, runs it, writes the gob [Result] to fd 3
// and returns 0; 3 on a malformed request, 2 when par2go cannot load.
// Closing stdin cancels the run. It sets up no flags, logging, tracing, NATS
// or Kubernetes.
//
// "agent par2-repair --self-check" loads par2go, prints "par2go <library>"
// and returns 0, or prints the error (naming PAR2GO_LIB) and returns 1.
func ChildMain(args []string) int {
	if len(args) > 0 && args[0] == "--self-check" {
		if err := par2.Available(); err != nil {
			fmt.Fprintf(os.Stderr, "par2go is unavailable (PAR2GO_LIB=%q): %v\n", os.Getenv("PAR2GO_LIB"), err)
			return 1
		}
		fmt.Println("par2go", Library())
		return 0
	}
	// A cgroup OOM then picks the child, not the engine. Raising it needs no
	// privilege; best effort all the same.
	_ = os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0)

	var n uint32
	if err := binary.Read(os.Stdin, binary.BigEndian, &n); err != nil || n > maxResult {
		fmt.Fprintln(os.Stderr, "par2child: malformed request")
		return 3
	}
	var req Request
	if err := gob.NewDecoder(io.LimitReader(os.Stdin, int64(n))).Decode(&req); err != nil {
		fmt.Fprintln(os.Stderr, "par2child: malformed request:", err)
		return 3
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The parent closes stdin to cancel; a parent that died closes it too.
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()

	out := os.NewFile(3, "par2-result")
	write := func(r Result) int {
		if err := gob.NewEncoder(out).Encode(r); err != nil {
			fmt.Fprintln(os.Stderr, "par2child: write the result:", err)
			return 3
		}
		return 0
	}
	if err := par2.Available(); err != nil {
		_ = write(Result{ErrKind: ErrKindUnavailable, Err: err.Error()})
		return 2
	}
	switch req.Op {
	case OpProbe:
		return write(Result{Library: Library()})
	case OpRepair:
		res, _ := InProcess{}.Run(ctx, req)
		return write(res)
	}
	fmt.Fprintln(os.Stderr, "par2child: unknown op", req.Op)
	return 3
}

// RunIfChild runs [ChildMain] and exits when this process was started as the
// repair child (os.Args[1] == ChildCommand). cmd/agent calls it first in
// main, and so must the TestMain of every test binary whose code can build a
// usenet engine: the child is os.Executable(), which in a test is the test
// binary, and without the dispatch it would run the package's tests again.
func RunIfChild() {
	if len(os.Args) > 1 && os.Args[1] == ChildCommand {
		os.Exit(ChildMain(os.Args[2:]))
	}
}
