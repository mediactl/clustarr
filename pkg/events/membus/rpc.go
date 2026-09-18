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

package membus

import (
	"context"
	"encoding/json"
	"fmt"
)

// responder is one in-process request/reply handler.
type responder struct {
	subject string
	queue   string
	handle  func(ctx context.Context, data []byte) ([]byte, error)
}

// call encodes in, runs the handler on its own goroutine so a slow responder
// cannot outlive the caller's context, and decodes the reply into out.
func (r *responder) call(ctx context.Context, in, out any) error {
	var req []byte
	switch v := in.(type) {
	case nil:
	case []byte:
		req = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("membus: encode request for %s: %w", r.subject, err)
		}
		req = b
	}

	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := r.handle(ctx, req)
		ch <- result{data: data, err: err}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return fmt.Errorf("membus: %s: %w", r.subject, res.err)
		}
		switch v := out.(type) {
		case nil:
			return nil
		case *[]byte:
			*v = res.data
			return nil
		default:
			if err := json.Unmarshal(res.data, out); err != nil {
				return fmt.Errorf("membus: decode reply from %s: %w", r.subject, err)
			}
			return nil
		}
	}
}
