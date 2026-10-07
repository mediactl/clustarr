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

package retrigger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
)

func TestImportAnnotationsChanged(t *testing.T) {
	p := ImportAnnotationsChanged()
	dl := func(ann map[string]string) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{ObjectMeta: metav1.ObjectMeta{Annotations: ann}}
	}
	assert.False(t, p.Create(event.CreateEvent{Object: dl(nil)}))
	assert.True(t, p.Create(event.CreateEvent{Object: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a"})}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: dl(nil), ObjectNew: dl(map[string]string{importtarget.AnnotationImportOverride: "true"})}))
	assert.True(t, p.Update(event.UpdateEvent{
		ObjectOld: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a"}),
		ObjectNew: dl(map[string]string{importtarget.AnnotationImportTarget: "album/b"}),
	}))
	assert.False(t, p.Update(event.UpdateEvent{
		ObjectOld: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a", "other": "1"}),
		ObjectNew: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a", "other": "2"}),
	}),
		"only the two import annotations re-trigger; a status write or another annotation must not loop it")
	assert.False(t, p.Delete(event.DeleteEvent{Object: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a"})}))
}

func TestMessageID(t *testing.T) {
	a := MessageID("ns", "dl", "uid", "album/a", "")
	assert.NotEqual(t, a, MessageID("ns", "dl", "uid", "album/b", ""), "a changed instruction is a new message")
	assert.NotEqual(t, a, MessageID("ns", "dl", "uid", "album/a", "true"))
	assert.Equal(t, a, MessageID("ns", "dl", "uid", "album/a", ""), "the same instruction dedups")
	assert.NotEqual(t, "ns/dl:uid:import", a, "never grabarr's own completion message id")
}
