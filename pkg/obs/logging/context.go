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

package logging

import (
	"context"
	"log/slog"
)

// ctxKey is the type of the context key this package uses to carry a
// *slog.Logger. It is unexported so no value stored under it can collide
// with a key any other package defines, even one that also happens to be an
// empty struct{} -- Go compares context keys by (type, value), and no other
// package can name this type.
type ctxKey struct{}

// FromContext returns the logger carried by ctx.
//
// When ctx carries none -- most often because it descends from
// context.Background() rather than from a NewContext call -- FromContext
// returns a logger backed by slog.DiscardHandler rather than nil or a
// package-level default. Clustarr's logger travels only in the context (see
// package doc), so this is the fallback every call site gets automatically
// instead of a nil check or a global.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return discardLogger
}

// discardLogger is FromContext's fallback. It is safe to share: slog.Logger
// is immutable and slog.DiscardHandler.Enabled always reports false, so
// every call is a cheap no-op regardless of how many goroutines hold it
// concurrently.
var discardLogger = slog.New(slog.DiscardHandler)

// NewContext returns a copy of ctx carrying logger, retrievable with
// FromContext. It does not modify ctx.
func NewContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// With returns a new context whose logger is FromContext(ctx).With(args...).
//
// It never mutates ctx or the *slog.Logger already attached to it: the
// returned context is a distinct value carrying a distinct logger, so a
// handler that enriches its own context for the rest of a call (for example
// with a resource's name) cannot leak those attributes onto a sibling
// goroutine that still holds the parent context.
func With(ctx context.Context, args ...any) context.Context {
	return NewContext(ctx, FromContext(ctx).With(args...))
}
