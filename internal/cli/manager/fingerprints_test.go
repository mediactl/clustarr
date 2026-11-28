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

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The fingerprint reap's lookup answers from mfindex.ProbeHash: a hash a
// MediaFile has, or one it had, is referenced whatever its namespace; any
// other is not; and a file being probed again still names the hashes it had.
func TestAFingerprintIsReferencedByTheHashAFileHasOrHad(t *testing.T) {
	file := func(ns, name, hash string, former ...string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Status:     catalogv1alpha1.MediaFileStatus{ProbeHash: hash, FormerProbeHashes: former},
		}
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.MediaFile{}, mfindex.ProbeHash, mfindex.ProbeHashValues).
		WithObjects(
			file("media", "a", "hash-a", "hash-a-before", "hash-a-first"),
			file("other", "b", "hash-b"),
			file("media", "reprobing", "", "had-only"),
		).Build()
	referenced := fingerprintReferenced(c)
	for hash, want := range map[string]bool{
		"hash-a": true, "hash-a-before": true, "hash-a-first": true, "hash-b": true,
		"had-only": true, "hash-gone": false, "": false,
	} {
		got, err := referenced(t.Context(), hash)
		require.NoError(t, err, hash)
		require.Equal(t, want, got, hash)
	}
}
