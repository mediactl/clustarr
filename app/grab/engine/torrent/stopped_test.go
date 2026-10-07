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

package torrent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

func TestStallTimeoutResolvesTheCRDDefault(t *testing.T) {
	assert.Equal(t, downloadv1alpha1.DefaultStallTimeout, StallTimeout(nil))
	assert.Equal(t, downloadv1alpha1.DefaultStallTimeout, StallTimeout(&downloadv1alpha1.TorrentSpec{}))
	assert.Equal(t, 2*time.Hour, StallTimeout(&downloadv1alpha1.TorrentSpec{StallTimeout: &metav1.Duration{Duration: 2 * time.Hour}}))
	assert.Zero(t, StallTimeout(&downloadv1alpha1.TorrentSpec{StallTimeout: &metav1.Duration{}}), "0s disables")
}
