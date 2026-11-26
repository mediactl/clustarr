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

package manager

import (
	"testing"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/library/ctrlwire"
	"github.com/mediactl/clustarr/test/librarytest"
)

// newManager builds the manager Run builds -- the same options, the same
// row cache fed from the library store -- on the test binary's library
// database.
func newManager(t testing.TB, cfg *rest.Config, o Options) (ctrl.Manager, error) {
	t.Helper()
	store := librarytest.For(t, cfg)
	opts, err := libraryOptions(o, store)
	if err != nil {
		return nil, err
	}
	mgr, err := ctrl.NewManager(cfg, opts)
	if err != nil {
		return nil, err
	}
	return ctrlwire.Manager(mgr, store), nil
}
