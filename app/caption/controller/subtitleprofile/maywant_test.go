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

package subtitleprofile

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A SubtitleRequest is ensured only for a probed file a subtitle may be
// wanted for (subtitlerequest.MayWant): 11,600 of the owner's 13,308 were
// Satisfied with no items, and 1,429 more were Blocked waiting for a probe.
// A request the pre-check now skips is deleted only when it tracks nothing
// and carries no user override.
func TestReconcileEnsuresRequestsOnlyWhereASubtitleMayBeWanted(t *testing.T) {
	sp := profileAt("english", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{
		Default:   true,
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en", AudioExclude: true}},
	})
	probed := func(name, audio string) (*catalogv1alpha1.Movie, *catalogv1alpha1.MediaFile) {
		mf := movieFile(name, nil)
		mf.UID = types.UID("uid-" + name) // the fake client assigns none; owner references need one
		if audio != "" {
			mf.Status.ProbeHash = "p-" + name
			mf.Status.MediaInfo = &commonv1.MediaInfo{VideoCodec: "h264", Audio: []commonv1.AudioStream{{Codec: "aac", Language: audio}}}
		}
		return &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"}}, mf
	}
	request := func(name string, mutate func(*subtitlev1alpha1.SubtitleRequest)) *subtitlev1alpha1.SubtitleRequest {
		sr := &subtitlev1alpha1.SubtitleRequest{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       subtitlev1alpha1.SubtitleRequestSpec{MediaFileRef: name, ProfileRef: "english"},
		}
		if mutate != nil {
			mutate(sr)
		}
		return sr
	}

	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&subtitlev1alpha1.SubtitleProfile{}, &subtitlev1alpha1.SubtitleRequest{}, &catalogv1alpha1.MediaFile{}).
		WithObjects(&sp)
	for _, f := range []struct{ name, audio string }{
		{"english-audio", "eng"},
		{"japanese-audio", "jpn"},
		{"unprobed", ""},
		{"stale-empty", "eng"},
		{"has-items", "eng"},
		{"user-forced", "eng"},
	} {
		m, mf := probed(f.name, f.audio)
		b = b.WithObjects(m, mf)
	}
	b = b.WithObjects(
		request("stale-empty", nil),
		request("has-items", func(sr *subtitlev1alpha1.SubtitleRequest) {
			sr.Status.Items = []subtitlev1alpha1.SubtitleItem{{LangKey: "en"}}
		}),
		request("user-forced", func(sr *subtitlev1alpha1.SubtitleRequest) { sr.Spec.ForceSearch = true }),
	)
	c := b.Build()
	r := NewReconciler(c, k8s.MustNewScheme(), nil)

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "english"}})
	require.NoError(t, err)

	var list subtitlev1alpha1.SubtitleRequestList
	require.NoError(t, c.List(context.Background(), &list))
	var names []string
	for _, sr := range list.Items {
		names = append(names, sr.Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"has-items", "japanese-audio", "user-forced"}, names,
		"only Japanese audio wants English subtitles; the empty stale request goes; items and overrides stay; nothing for an unprobed file")
}
