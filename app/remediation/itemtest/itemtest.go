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

// Package itemtest runs the remediation loop's item path without its file
// planners, for the item packages' envtests (loop spec §3.12). It is test
// support: only _test.go files import it, so no binary links it.
package itemtest

import (
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/remediation"
)

// Start registers on mgr one controller, "itemtest", that runs items exactly
// as the loop runs them -- remediation.ItemReconciler's sources (each item's
// Watches and the item arm of the MediaFile source) and its dispatch -- and
// none of the loop's file planners, so a test may write a MediaFile's status
// itself and watch the item follow. The name check is skipped, so every
// test in a binary may start one. The caller registers the indexes the
// items read (mfindex.Register and the item packages' RegisterIndexes) and
// starts mgr.
func Start(mgr manager.Manager, items map[remediation.KeyKind]rollup.Item) error {
	ir := &remediation.ItemReconciler{Items: items}
	return ir.Watch(builder.TypedControllerManagedBy[remediation.Key](mgr).Named("itemtest")).
		WithOptions(controller.TypedOptions[remediation.Key]{SkipNameValidation: new(true), RecoverPanic: new(true)}).
		Complete(ir)
}
