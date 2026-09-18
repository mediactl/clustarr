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

package version

import "fmt"

// Version is the semantic version of the binary. It is overridden at link
// time with -X github.com/mediactl/clustarr/pkg/version.Version=<version>.
var Version = "0.0.0-dev"

// Commit is the git revision the binary was built from. It is overridden at
// link time with -X github.com/mediactl/clustarr/pkg/version.Commit=<sha>.
var Commit = "unknown"

// String renders the build identity as "<version>+<commit>". It is also the
// value used for the Clustarr-Source message header, prefixed by the service
// name.
func String() string {
	return fmt.Sprintf("%s+%s", Version, Commit)
}
