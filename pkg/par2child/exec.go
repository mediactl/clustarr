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
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	par2 "github.com/mediactl/par2go"
)

// Exec runs par2go in a child of this binary (§8.4.3). The zero value is
// what the engine uses: os.Executable() par2-repair, with only the child's
// environment and the default grace.
type Exec struct {
	// Path is the binary to run; "" is os.Executable().
	Path string
	// Args are its arguments; nil is []string{ChildCommand}.
	Args []string
	// Env is its whole environment; nil is childEnv().
	Env []string
	// Grace is how long the child may take to stop once ctx is done before
	// it is killed; 0 is DefaultGrace.
	Grace time.Duration
}

var _ Runner = Exec{}

var unreaped atomic.Int64

// Unreaped counts children whose deadline, grace and reap slack passed
// without them exiting (a read blocked in the kernel, D state); the usenet
// engine exports it as clustarr_par2_unreaped_children. Each is reaped, and
// uncounted, once its I/O returns.
func Unreaped() int { return int(unreaped.Load()) }

// Run runs req in a child.
func (e Exec) Run(ctx context.Context, req Request) (Result, error) { return e.run(ctx, req) }

// Probe loads par2go in a child, so the engine process itself never dlopens
// it.
func (e Exec) Probe(ctx context.Context) error {
	res, err := e.run(ctx, Request{Op: OpProbe})
	if err != nil {
		return err
	}
	return res.Error()
}

// run is the module's one production exec (spec §4.5.6), and its target is
// this binary.
//
// Cancellation is cooperative first: os/exec calls Cancel when ctx is done,
// which closes the child's stdin, and the child cancels par2go. WaitDelay
// then kills a child still running Grace later, and closes the output pipes,
// so nothing the child holds open keeps Wait blocked (the par2 deadline test
// once hung on exactly that). The child runs in its own process group, so a
// terminal's SIGINT to the engine's group cannot kill it before the parent
// cancels it; it starts no processes of its own, so killing it is killing
// all of it.
func (e Exec) run(ctx context.Context, req Request) (Result, error) {
	path := e.Path
	if path == "" {
		p, err := os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("%w: find this binary: %w", par2.ErrUnavailable, err)
		}
		path = p
	}
	args, env, grace := e.Args, e.Env, e.Grace
	if args == nil {
		args = []string{ChildCommand}
	}
	if env == nil {
		env = childEnv(os.Getenv)
	}
	if grace == 0 {
		grace = DefaultGrace
	}

	var body bytes.Buffer
	if err := gob.NewEncoder(&body).Encode(req); err != nil {
		return Result{}, fmt.Errorf("par2child: encode the request: %w", err)
	}
	if body.Len() > maxResult {
		return Result{}, fmt.Errorf("par2child: the request is %d bytes, over the %d the child reads", body.Len(), maxResult)
	}

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("par2child: stdin: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return Result{}, fmt.Errorf("par2child: result pipe: %w", err)
	}
	defer func() { _ = r.Close() }()
	cmd.ExtraFiles = []*os.File{w} // fd 3 in the child
	tail := &tailBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = tail, tail
	cmd.Cancel = func() error { return stdin.Close() } // the child cancels par2go when stdin closes
	cmd.WaitDelay = grace                              // then os/exec kills it
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		_ = stdin.Close()
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return Result{}, fmt.Errorf("%w: start %s: %w", par2.ErrUnavailable, path, err)
		}
		return Result{}, fmt.Errorf("par2child: start %s: %w", path, err)
	}
	_ = w.Close()

	// stdin stays open after the request: closing it is the cancel. A
	// failed write means the child is already gone, which Wait reports.
	var frame [4]byte
	binary.BigEndian.PutUint32(frame[:], uint32(body.Len()))
	if _, err := stdin.Write(frame[:]); err == nil {
		_, _ = stdin.Write(body.Bytes())
	}

	got := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(r, maxResult))
		got <- b
	}()
	// Wait runs apart because it calls Process.Wait first, which a child in
	// uninterruptible I/O blocks however it was killed.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		t := time.NewTimer(grace + reapSlack)
		defer t.Stop()
		select {
		case waitErr = <-waited:
		case <-t.C:
			unreaped.Add(1)
			go func() { <-waited; unreaped.Add(-1) }()
			return Result{}, fmt.Errorf("%w: %w (still not reaped %s after its deadline; it is reaped when its I/O returns)",
				ErrChildDied, ctx.Err(), grace+reapSlack)
		}
	}

	var res Result
	decodeErr := gob.NewDecoder(bytes.NewReader(<-got)).Decode(&res)
	switch {
	case ctx.Err() != nil:
		return res, fmt.Errorf("par2child: %w", ctx.Err())
	case decodeErr != nil:
		return Result{}, fmt.Errorf("%w: %v: %s", ErrChildDied, waitErr, strings.TrimSpace(tail.String()))
	}
	return res, res.Error()
}

// childEnv is the child's whole environment (§8.4.3): what par2go and the Go
// runtime read, and nothing else -- no credentials, no NATS or Kubernetes
// address.
func childEnv(getenv func(string) string) []string {
	var env []string
	for _, k := range []string{"PAR2GO_LIB", "LD_LIBRARY_PATH", "TMPDIR", "GODEBUG", "GOMAXPROCS"} {
		if v := getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
