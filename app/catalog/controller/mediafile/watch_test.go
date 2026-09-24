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

package mediafile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestExtractTranscodeJobPhase(t *testing.T) {
	tj := &transcodev1alpha1.TranscodeJob{Status: transcodev1alpha1.TranscodeJobStatus{Phase: transcodev1alpha1.TranscodeJobPhaseSucceeded}}
	assert.Equal(t, "Succeeded", extractTranscodeJobPhase(tj))

	other := &subtitlev1alpha1.SubtitleRequest{}
	assert.Equal(t, "", extractTranscodeJobPhase(other), "wrong type returns the zero value, never panics")
}

func TestMediaFileForTranscodeJob(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "inception-abc12345", Namespace: "media"},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: "inception-abc1234567"},
	}

	reqs := (&Reconciler{Client: c}).mediaFileForTranscodeJob(t.Context(), tj)

	want := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "media", Name: "inception-abc1234567"}}}
	assert.Equal(t, want, reqs)
}
