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
	"context"
	"testing"

	par2 "github.com/mediactl/par2go"
	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/par2child"
)

// The sentinel crosses gob as a kind and is rebuilt in the parent, so
// errors.Is works there (§8.4).
func TestResultErrorWrapsTheLibrarySentinel(t *testing.T) {
	for kind, want := range map[par2child.ErrKind]error{
		par2child.ErrKindUnavailable:              par2.ErrUnavailable,
		par2child.ErrKindInvalidOptions:           par2.ErrInvalidOptions,
		par2child.ErrKindInsufficientCriticalData: par2.ErrInsufficientCriticalData,
		par2child.ErrKindIO:                       par2.ErrIO,
		par2child.ErrKindMemory:                   par2.ErrMemory,
		par2child.ErrKindRepairFailed:             par2.ErrRepairFailed,
		par2child.ErrKindLogic:                    par2.ErrLogic,
		par2child.ErrKindCancelled:                context.Canceled,
	} {
		err := par2child.Result{ErrKind: kind, Err: "detail"}.Error()
		assert.ErrorIs(t, err, want, "kind %d", kind)
		assert.ErrorContains(t, err, "detail")
	}
	assert.NoError(t, par2child.Result{}.Error())
	assert.EqualError(t, par2child.Result{ErrKind: par2child.ErrKindOther, Err: "odd"}.Error(), "par2child: odd")
}
