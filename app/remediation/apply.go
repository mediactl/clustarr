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

package remediation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// statusApply is the whole status as an apply configuration: marshalled and
// unmarshalled, so every field the stored object holds is declared on every
// apply and conditions and atomic lists are set once (§3.7 "The status is
// complete by construction"): no branch, early return or planner failure
// can release a field.
func statusApply(name, ns string, s *catalogv1alpha1.MediaFileStatus) (*catalogac.MediaFileApplyConfiguration, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("remediation: marshal status: %w", err)
	}
	sac := &catalogac.MediaFileStatusApplyConfiguration{}
	if err := json.Unmarshal(b, sac); err != nil {
		return nil, fmt.Errorf("remediation: status apply configuration: %w", err)
	}
	return catalogac.MediaFile(name, ns).WithStatus(sac), nil
}

// normalize is s with every list sorted by its key and empty lists nil, so
// an unchanged render compares equal to the stored status. A later wave
// adding a keyed list adds its sort here.
func normalize(s *catalogv1alpha1.MediaFileStatus) *catalogv1alpha1.MediaFileStatus {
	out := s.DeepCopy()
	if len(out.Conditions) == 0 {
		out.Conditions = nil
	}
	slices.SortFunc(out.Conditions, func(a, b metav1.Condition) int { return strings.Compare(a.Type, b.Type) })
	if len(out.Sidecars) == 0 {
		out.Sidecars = nil
	}
	slices.SortFunc(out.Sidecars, func(a, b catalogv1alpha1.Sidecar) int { return strings.Compare(a.Path, b.Path) })
	if st := out.Subtitles; st != nil {
		if len(st.Items) == 0 {
			st.Items = nil
		}
		slices.SortFunc(st.Items, func(a, b catalogv1alpha1.SubtitleItemStatus) int { return strings.Compare(a.LangKey, b.LangKey) })
		if len(st.Wanted) == 0 {
			st.Wanted = nil
		}
		slices.Sort(st.Wanted)
	}
	return out
}

// viewReader serves the object the planners ran on, at the resourceVersion
// and generation a main apply in this pass returned. The loop never replans
// from an uncached read: one attempt, then a requeue.
type viewReader struct {
	obj *catalogv1alpha1.MediaFile
	rv  string
	gen int64
}

func (r viewReader) Get(_ context.Context, key client.ObjectKey, out client.Object, _ ...client.GetOption) error {
	mf, ok := out.(*catalogv1alpha1.MediaFile)
	if !ok || key != client.ObjectKeyFromObject(r.obj) {
		return fmt.Errorf("remediation: the view serves only MediaFile %s", client.ObjectKeyFromObject(r.obj))
	}
	r.obj.DeepCopyInto(mf)
	mf.ResourceVersion, mf.Generation = r.rv, r.gen
	return nil
}

func (viewReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("remediation: the view serves no List")
}

func newMediaFile() *catalogv1alpha1.MediaFile { return &catalogv1alpha1.MediaFile{} }
