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

package facade

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// APIKeyField is the data key the manager stores a generated facade API key
// under, in the Secret --facade-api-key-secret names. It is only the
// generated key's name: the facade accepts the value of EVERY non-blank
// entry in the Secret, so an operator rotates without downtime by adding a
// second entry, moving clients over, then deleting the first.
const APIKeyField = "apikey"

// SecretComponent is the generated Secret's app.kubernetes.io/component
// label: the index domain's name.
const SecretComponent = "index"

// KeysOf returns the trimmed, non-blank values of sec's data, in key order so
// a re-read builds the same list.
func KeysOf(sec *corev1.Secret) []string {
	if sec == nil {
		return nil
	}
	names := make([]string, 0, len(sec.Data))
	for n := range sec.Data {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		if v := strings.TrimSpace(string(sec.Data[n])); v != "" {
			out = append(out, v)
		}
	}
	return out
}
