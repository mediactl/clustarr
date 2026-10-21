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

package k8s_test

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// fresh builds each string anew, as the decoder does for every object.
func fresh(s string) string { return strings.Clone(s) }

func episode(name string) *catalogv1alpha1.Episode {
	return &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: name, ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "catalog"}}},
		Status: catalogv1alpha1.EpisodeStatus{Conditions: []metav1.Condition{{
			Type: fresh("WrongLanguage"), Status: metav1.ConditionStatus(fresh("False")),
			Reason: fresh("LanguageOK"), Message: fresh("no evidence the file's audio is wrong"),
		}}},
	}
}

// TestCacheTransformPreservesValues: the transform only shares storage; a
// reassert from the cache declares exactly what the apiserver holds
// (control-plane placement design §5.4.1 item 5).
func TestCacheTransformPreservesValues(t *testing.T) {
	ep := episode("s01e01")
	want := ep.Status.DeepCopy()

	out, err := k8s.CacheTransform(ep)

	require.NoError(t, err)
	assert.Equal(t, *want, out.(*catalogv1alpha1.Episode).Status)
}

func TestCacheTransformStripsManagedFields(t *testing.T) {
	out, err := k8s.CacheTransform(episode("s01e01"))
	require.NoError(t, err)
	assert.Nil(t, out.(*catalogv1alpha1.Episode).ManagedFields)
}

// TestCacheTransformSharesConditionStrings: 21,141 episodes carried their
// own copy of "no evidence the file's audio is wrong" (talos, 2026-10-21).
func TestCacheTransformSharesConditionStrings(t *testing.T) {
	a, b := episode("s01e01"), episode("s01e02")
	require.NotSame(t, unsafe.StringData(a.Status.Conditions[0].Message), unsafe.StringData(b.Status.Conditions[0].Message))

	_, err := k8s.CacheTransform(a)
	require.NoError(t, err)
	_, err = k8s.CacheTransform(b)
	require.NoError(t, err)

	ca, cb := a.Status.Conditions[0], b.Status.Conditions[0]
	assert.Same(t, unsafe.StringData(ca.Message), unsafe.StringData(cb.Message))
	assert.Same(t, unsafe.StringData(ca.Reason), unsafe.StringData(cb.Reason))
	assert.Same(t, unsafe.StringData(ca.Type), unsafe.StringData(cb.Type))
	assert.Same(t, unsafe.StringData(string(ca.Status)), unsafe.StringData(string(cb.Status)))
}

func TestCacheTransformInternsMediaFileConditions(t *testing.T) {
	mk := func() *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{Conditions: []metav1.Condition{{
			Type: fresh("Ready"), Status: metav1.ConditionStatus(fresh("True")), Reason: fresh("Ready"), Message: fresh("file present and probed"),
		}}}}
	}
	a, b := mk(), mk()
	_, _ = k8s.CacheTransform(a)
	_, _ = k8s.CacheTransform(b)
	assert.Same(t, unsafe.StringData(a.Status.Conditions[0].Message), unsafe.StringData(b.Status.Conditions[0].Message))
}

func TestCacheTransformPassesOtherObjectsThrough(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "x"}}}}
	out, err := k8s.CacheTransform(pod)
	require.NoError(t, err)
	assert.Equal(t, "p", out.(*corev1.Pod).Name)
	assert.Nil(t, out.(*corev1.Pod).ManagedFields, "managedFields still stripped from every kind")
}
