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

package usenet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The ledger hands out at most the free connections under the account's
// limit, never a partial grant below min, and a returned grant frees them.
func TestLocalLedgerTakesAtMostTheFreeConnections(t *testing.T) {
	ctx := context.Background()
	l := NewLocalLedger()
	const acct = "news.example:563:me"

	n, err := l.Take(ctx, acct, 10, Grant{Holder: "a", Server: "x"}, 6, 1)
	require.NoError(t, err)
	require.Equal(t, 6, n)

	n, err = l.Take(ctx, acct, 10, Grant{Holder: "b", Server: "x"}, 6, 1)
	require.NoError(t, err)
	require.Equal(t, 4, n, "b gets what is left")

	n, err = l.Take(ctx, acct, 10, Grant{Holder: "c", Server: "x"}, 6, 5)
	require.NoError(t, err)
	require.Zero(t, n, "nothing is free, and a grant under min is not taken")

	held, err := l.Held(ctx, acct)
	require.NoError(t, err)
	require.Equal(t, 10, held)

	require.NoError(t, l.Give(ctx, acct, "a"))
	n, err = l.Take(ctx, acct, 10, Grant{Holder: "c", Server: "x"}, 6, 5)
	require.NoError(t, err)
	require.Equal(t, 6, n)

	require.NoError(t, l.Give(ctx, acct, "never-held"), "giving back an absent grant is not an error")
}

// A holder that already holds a grant on the account gets that grant's size
// back, so a re-take after a restart of the loop never doubles it.
func TestLocalLedgerTakeIsIdempotentPerHolder(t *testing.T) {
	ctx := context.Background()
	l := NewLocalLedger()
	n, err := l.Take(ctx, "acct", 10, Grant{Holder: "a", Server: "x"}, 6, 1)
	require.NoError(t, err)
	require.Equal(t, 6, n)
	n, err = l.Take(ctx, "acct", 10, Grant{Holder: "a", Server: "x"}, 8, 1)
	require.NoError(t, err)
	require.Equal(t, 6, n)
	held, err := l.Held(ctx, "acct")
	require.NoError(t, err)
	require.Equal(t, 6, held)
}

func TestShareIsAtLeastOne(t *testing.T) {
	require.Equal(t, 50, share(100, 2))
	require.Equal(t, 1, share(1, 4))
	require.Equal(t, 1, share(0, 2))
	require.Equal(t, 8, share(8, 0))
}

func TestProviderAccountIsHostPortUser(t *testing.T) {
	p := Provider{Name: "frugal", Host: "news.frugal.example", Port: 563, Username: "me@x"}
	require.Equal(t, "news.frugal.example:563:me@x", p.Account())
}
