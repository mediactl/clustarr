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

package tuner_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/livetv/tuner"
)

// TestListeningClosesOnceTheListenerIsBound: the leader publishes itself as
// the livetv endpoint only once its tuner listens (control-plane placement
// design §5.4), so Listening must close after the bind, not before Start.
func TestListeningClosesOnceTheListenerIsBound(t *testing.T) {
	tn := tuner.New(tuner.Options{BindAddress: "127.0.0.1:0", Store: newStore(t)})
	select {
	case <-tn.Listening():
		t.Fatal("Listening closed before Start bound anything")
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tn.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	select {
	case <-tn.Listening():
	case <-time.After(2 * time.Second):
		t.Fatal("Listening never closed after Start")
	}
}

// TestListeningNeverClosesWhenDisabled: with no bind address there is no
// listener, so there must be no endpoint (the plan's Review Focus 3).
func TestListeningNeverClosesWhenDisabled(t *testing.T) {
	tn := tuner.New(tuner.Options{Store: newStore(t)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tn.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	select {
	case <-tn.Listening():
		t.Fatal("Listening closed with no bind address")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestListeningNeverClosesWhenTheBindFails: an address that cannot be bound
// is a failed Start, never a published endpoint.
func TestListeningNeverClosesWhenTheBindFails(t *testing.T) {
	taken := tuner.New(tuner.Options{BindAddress: "256.0.0.1:1", Store: newStore(t)})
	err := taken.Start(context.Background())
	require.Error(t, err)
	select {
	case <-taken.Listening():
		t.Fatal("Listening closed although the bind failed")
	default:
	}
}
