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
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s/rowcache"
	"github.com/mediactl/clustarr/pkg/segments/fingerprintmeta"
)

// fingerprintReferenced answers the maintenance loop's fingerprint reap
// (fingerprintmeta.ReapStore) from the manager's cache: whether a MediaFile
// has, or had, a probe hash, through mfindex.ProbeHash and as a key only, so
// no file is decoded (a list of every MediaFile decodes every row).
func fingerprintReferenced(c client.Reader) fingerprintmeta.Referenced {
	return func(ctx context.Context, probeHash string) (bool, error) {
		keys, err := rowcache.Keys(ctx, c, &catalogv1alpha1.MediaFileList{},
			client.MatchingFields{mfindex.ProbeHash: probeHash}, client.Limit(1))
		return len(keys) > 0, err
	}
}
