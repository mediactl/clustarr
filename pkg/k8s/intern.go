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

package k8s

import (
	"unique"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

var stripManagedFields = cache.TransformStripManagedFields()

// CacheTransform is every cache's transform: it strips managedFields, then
// makes the Episode and MediaFile conditions' strings share storage. Five
// near-constant conditions per episode were a copy each in every replica's
// cache -- "no evidence the file's audio is wrong" 21,141 times on talos
// (control-plane placement design §5.4.1 item 5). It changes no value, so a
// status reasserted from the cache declares exactly what the apiserver
// holds; a transform that removed a field would release it on that reassert.
func CacheTransform(obj any) (any, error) {
	obj, err := stripManagedFields(obj)
	if err != nil {
		return obj, err
	}
	switch o := obj.(type) {
	case *catalogv1alpha1.Episode:
		internConditions(o.Status.Conditions)
	case *catalogv1alpha1.MediaFile:
		internConditions(o.Status.Conditions)
	}
	return obj, nil
}

func internConditions(cs []metav1.Condition) {
	for i := range cs {
		c := &cs[i]
		c.Type = unique.Make(c.Type).Value()
		c.Status = metav1.ConditionStatus(unique.Make(string(c.Status)).Value())
		c.Reason = unique.Make(c.Reason).Value()
		c.Message = unique.Make(c.Message).Value()
	}
}
