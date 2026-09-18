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

package logging_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

func TestFromContextReturnsADiscardLoggerWhenAbsent(t *testing.T) {
	l := logging.FromContext(context.Background())
	require.NotNil(t, l)
	l.Info("must not panic")
}

func TestNewContextRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	want := logging.New(logging.Options{Format: "json", Output: &buf})
	ctx := logging.NewContext(context.Background(), want)
	logging.FromContext(ctx).Info("hello", "k", "v")
	require.Contains(t, buf.String(), `"msg":"hello"`)
	require.Contains(t, buf.String(), `"k":"v"`)
}

func TestWithAttachesAttributesForLaterCalls(t *testing.T) {
	var buf bytes.Buffer
	ctx := logging.NewContext(context.Background(), logging.New(logging.Options{Format: "json", Output: &buf}))
	ctx = logging.With(ctx, "movie", "tt0111161")
	logging.FromContext(ctx).Info("reconciling")
	require.Contains(t, buf.String(), `"movie":"tt0111161"`)
}

// TestWithNeverMutatesTheParentContext pins down the behaviour the design
// amendment implies but does not spell out: With must hand back a new
// context, never rewrite the logger the parent context already carries. A
// handler that enriches its own context must not leak attributes onto a
// sibling goroutine still holding the parent.
func TestWithNeverMutatesTheParentContext(t *testing.T) {
	var buf bytes.Buffer
	parent := logging.NewContext(context.Background(), logging.New(logging.Options{Format: "json", Output: &buf}))

	child := logging.With(parent, "movie", "tt0111161")
	require.NotSame(t, parent, child)

	logging.FromContext(parent).Info("via parent")
	require.NotContains(t, buf.String(), "movie")

	logging.FromContext(child).Info("via child")
	require.Contains(t, buf.String(), `"movie":"tt0111161"`)
}
