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

package version_test

import (
	"strings"
	"testing"

	"github.com/mediactl/clustarr/pkg/version"
)

func TestStringJoinsVersionAndCommit(t *testing.T) {
	got := version.String()
	if !strings.Contains(got, version.Version) {
		t.Errorf("String() = %q, missing the version %q", got, version.Version)
	}
	if !strings.Contains(got, version.Commit) {
		t.Errorf("String() = %q, missing the commit %q", got, version.Commit)
	}
	if !strings.Contains(got, "+") {
		t.Errorf("String() = %q, want a <version>+<commit> form", got)
	}
}
