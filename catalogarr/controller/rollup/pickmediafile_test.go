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

package rollup_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
)

func TestPickMediaFile(t *testing.T) {
	t.Run("no files", func(t *testing.T) {
		assert.Nil(t, rollup.PickMediaFile(nil))
	})

	t.Run("prefers the one flagged Original regardless of age", func(t *testing.T) {
		older := metav1.NewTime(time.Now().Add(-time.Hour))
		newer := metav1.NewTime(time.Now())
		items := []catalogv1alpha1.MediaFile{
			{ObjectMeta: metav1.ObjectMeta{Name: "newer-not-original", CreationTimestamp: newer}, Spec: catalogv1alpha1.MediaFileSpec{Original: ptr.To(false)}},
			{ObjectMeta: metav1.ObjectMeta{Name: "older-original", CreationTimestamp: older}, Spec: catalogv1alpha1.MediaFileSpec{Original: ptr.To(true)}},
		}
		got := rollup.PickMediaFile(items)
		require.NotNil(t, got)
		assert.Equal(t, "older-original", got.Name)
	})

	t.Run("no file is Original: picks the most recently created", func(t *testing.T) {
		older := metav1.NewTime(time.Now().Add(-time.Hour))
		newer := metav1.NewTime(time.Now())
		items := []catalogv1alpha1.MediaFile{
			{ObjectMeta: metav1.ObjectMeta{Name: "older", CreationTimestamp: older}},
			{ObjectMeta: metav1.ObjectMeta{Name: "newer", CreationTimestamp: newer}},
		}
		got := rollup.PickMediaFile(items)
		require.NotNil(t, got)
		assert.Equal(t, "newer", got.Name)
	})

	// spec.original defaults to true, so two freshly imported files are both
	// flagged. The pick must not follow list order, or the item's fileRef
	// flaps between them from one reconcile (and one cache listing) to the
	// next.
	t.Run("two files flagged Original: the newest, whatever the list order", func(t *testing.T) {
		older := metav1.NewTime(time.Now().Add(-time.Hour))
		newer := metav1.NewTime(time.Now())
		a := catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "older", CreationTimestamp: older}, Spec: catalogv1alpha1.MediaFileSpec{Original: ptr.To(true)}}
		b := catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "newer", CreationTimestamp: newer}, Spec: catalogv1alpha1.MediaFileSpec{Original: ptr.To(true)}}
		for _, items := range [][]catalogv1alpha1.MediaFile{{a, b}, {b, a}} {
			got := rollup.PickMediaFile(items)
			require.NotNil(t, got)
			assert.Equal(t, "newer", got.Name)
		}
	})

	t.Run("a creation-time tie goes to the greater name, whatever the list order", func(t *testing.T) {
		at := metav1.NewTime(time.Now())
		a := catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "a", CreationTimestamp: at}}
		b := catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "b", CreationTimestamp: at}}
		for _, items := range [][]catalogv1alpha1.MediaFile{{a, b}, {b, a}} {
			got := rollup.PickMediaFile(items)
			require.NotNil(t, got)
			assert.Equal(t, "b", got.Name)
		}
	})
}
