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

// Package crdcheck holds the regression guards for the CustomResourceDefinitions:
// the install-time check that the generated CRDs load into a real apiserver,
// and two source walkers over api/ (G4-0) -- one for +kubebuilder:default
// markers a typed Go client can never reach, one for status lists without a
// MaxItems. Nothing imports it; it exists only for its tests.
package crdcheck
