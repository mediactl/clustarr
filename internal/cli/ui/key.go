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

package uicli

import (
	"fmt"
	"os"

	"github.com/mediactl/clustarr/internal/cli"
)

// MinArtSigningKeyBytes is the shortest ArtSigningKey accepted:
// HMAC-SHA256's own block of entropy.
const MinArtSigningKeyBytes = 32

// artSigningKey returns $CLUSTARR_ART_SIGNING_KEY, or nil when it is unset,
// which leaves ui to sign with a per-process key. A key too short to sign
// with is a startup error rather than a quietly weak signature.
func artSigningKey() ([]byte, error) {
	v := os.Getenv(cli.EnvArtSigningKey)
	if v == "" {
		return nil, nil
	}
	if len(v) < MinArtSigningKeyBytes {
		return nil, fmt.Errorf("$%s is %d bytes; it must be at least %d", cli.EnvArtSigningKey, len(v), MinArtSigningKeyBytes)
	}
	return []byte(v), nil
}
