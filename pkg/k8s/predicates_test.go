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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

func probeHash(o client.Object) string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok {
		return ""
	}
	return mf.Status.ProbeHash
}

func mediaFile(hash string, labels map[string]string) *catalogv1alpha1.MediaFile {
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "file", Namespace: "media", Labels: labels},
	}
	mf.Status.ProbeHash = hash
	return mf
}

func TestStatusFieldChangedIsTheProbeHashWatch(t *testing.T) {
	// §10: transcode's transcodeprofile watches MediaFile and reacts to
	// status.probeHash changing or the object being created -- nothing else.
	p := StatusFieldChanged(probeHash)

	if !p.Create(event.CreateEvent{Object: mediaFile("a", nil)}) {
		t.Error("create was filtered out")
	}
	if !p.Delete(event.DeleteEvent{Object: mediaFile("a", nil)}) {
		t.Error("delete was filtered out")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: mediaFile("a", nil), ObjectNew: mediaFile("b", nil)}) {
		t.Error("a probeHash change was filtered out")
	}
	if p.Update(event.UpdateEvent{ObjectOld: mediaFile("a", nil), ObjectNew: mediaFile("a", nil)}) {
		t.Error("an unrelated status write woke the watcher")
	}
}

func TestStatusFieldChangedIgnoresMetadataChurn(t *testing.T) {
	// The regression §14 names: "metadata refresh must not trigger transcode
	// reconcile". Labels, annotations and resourceVersion all move on a
	// metadata refresh while probeHash does not.
	p := StatusFieldChanged(probeHash)

	before := mediaFile("same", map[string]string{"catalog.clustarr.io/movie": "inception"})
	after := mediaFile("same", map[string]string{"catalog.clustarr.io/movie": "inception-2010"})
	after.ResourceVersion = "999"
	after.Annotations = map[string]string{"catalog.clustarr.io/refreshed": "now"}

	if p.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: after}) {
		t.Fatal("a metadata refresh woke a probeHash watcher")
	}
}

func TestStatusFieldChangedHandlesMissingObjects(t *testing.T) {
	p := StatusFieldChanged(probeHash)
	if p.Update(event.UpdateEvent{ObjectNew: mediaFile("a", nil)}) {
		t.Error("an update with no old object passed")
	}
	if p.Update(event.UpdateEvent{ObjectOld: mediaFile("a", nil)}) {
		t.Error("an update with no new object passed")
	}
}

// moviePhase and movieIn are the status-field fixtures. They were a
// Download's phase until the ADR-0019 cold turkey deleted that kind (A9.1);
// the predicates read any status field, and a Movie's phase is one.
func moviePhase(o client.Object) catalogv1alpha1.MoviePhase {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok {
		return ""
	}
	return m.Status.Phase
}

func movieIn(phase catalogv1alpha1.MoviePhase) *catalogv1alpha1.Movie {
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "inception-abc1234567", Namespace: "media"},
	}
	m.Status.Phase = phase
	return m
}

func TestStatusFieldInIsTheImporterWatch(t *testing.T) {
	// §10: a watch wakes only on entry into the phases it wants. It was
	// the importer's Download watch (Completed, Seeding or Failed) until
	// A9.1 deleted the kind; a Movie's file-bearing phases stand in.
	p := StatusFieldIn(moviePhase,
		catalogv1alpha1.MoviePhaseImported,
		catalogv1alpha1.MoviePhaseTranscoded,
		catalogv1alpha1.MoviePhaseCutoffUnmet,
	)

	if !p.Create(event.CreateEvent{Object: movieIn(catalogv1alpha1.MoviePhaseImported)}) {
		t.Error("an Imported create was filtered out")
	}
	if p.Create(event.CreateEvent{Object: movieIn(catalogv1alpha1.MoviePhaseDownloading)}) {
		t.Error("a Downloading create passed")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: movieIn(catalogv1alpha1.MoviePhaseDownloading), ObjectNew: movieIn(catalogv1alpha1.MoviePhaseImported)}) {
		t.Error("the transition into Imported was filtered out")
	}
	if p.Update(event.UpdateEvent{ObjectOld: movieIn(catalogv1alpha1.MoviePhaseImported), ObjectNew: movieIn(catalogv1alpha1.MoviePhaseImported)}) {
		t.Error("churn inside Imported woke the watch")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: movieIn(catalogv1alpha1.MoviePhaseImported), ObjectNew: movieIn(catalogv1alpha1.MoviePhaseTranscoded)}) {
		t.Error("a move between two wanted phases was filtered out")
	}
	if p.Update(event.UpdateEvent{ObjectOld: movieIn(catalogv1alpha1.MoviePhaseImported), ObjectNew: movieIn(catalogv1alpha1.MoviePhaseDownloading)}) {
		t.Error("a move out of a wanted phase passed")
	}
}

func TestLabelSelector(t *testing.T) {
	// A TranscodeProfile selects only the MediaFiles its labels name. (This
	// was an engine replica's Download selector until A9.1.)
	sel := metav1.LabelSelector{MatchLabels: map[string]string{
		catalogv1alpha1.LabelResolution: "2160p",
	}}
	p, err := LabelSelector(sel)
	if err != nil {
		t.Fatalf("LabelSelector: %v", err)
	}

	mine := mediaFile("a", map[string]string{catalogv1alpha1.LabelResolution: "2160p"})
	theirs := mediaFile("a", map[string]string{catalogv1alpha1.LabelResolution: "1080p"})

	if !p.Create(event.CreateEvent{Object: mine}) {
		t.Error("a selected MediaFile was filtered out")
	}
	if p.Create(event.CreateEvent{Object: theirs}) {
		t.Error("an unselected MediaFile passed")
	}
}

func TestLabelSelectorRejectsAMalformedSelector(t *testing.T) {
	_, err := LabelSelector(metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      "engine",
			Operator: "NotAnOperator",
		}},
	})
	if err == nil {
		t.Fatal("a malformed selector was accepted; a watch would then see every object")
	}
}

func TestHasLabel(t *testing.T) {
	p := HasLabel(catalogv1alpha1.LabelVideoCodec, "hevc")
	with := mediaFile("a", map[string]string{catalogv1alpha1.LabelVideoCodec: "hevc"})

	if !p.Create(event.CreateEvent{Object: with}) {
		t.Error("a matching object was filtered out")
	}
	if p.Create(event.CreateEvent{Object: mediaFile("a", nil)}) {
		t.Error("an unlabelled object passed")
	}
}

func TestDeleting(t *testing.T) {
	p := Deleting()
	now := metav1.Now()
	doomed := movieIn(catalogv1alpha1.MoviePhaseImported)
	doomed.DeletionTimestamp = &now
	doomed.Finalizers = []string{"catalog.clustarr.io/movie"}

	if !p.Update(event.UpdateEvent{ObjectOld: movieIn(catalogv1alpha1.MoviePhaseImported), ObjectNew: doomed}) {
		t.Error("a deleting object was filtered out")
	}
	if p.Update(event.UpdateEvent{ObjectOld: movieIn(catalogv1alpha1.MoviePhaseImported), ObjectNew: movieIn(catalogv1alpha1.MoviePhaseImported)}) {
		t.Error("a live object passed")
	}
}

func TestGenerationChangedIgnoresStatusWrites(t *testing.T) {
	p := GenerationChanged()

	old := movieIn(catalogv1alpha1.MoviePhaseDownloading)
	old.Generation = 3
	updated := movieIn(catalogv1alpha1.MoviePhaseImported)
	updated.Generation = 3

	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Error("a status write passed a generation predicate")
	}

	updated.Generation = 4
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Error("a spec change was filtered out")
	}
}

func TestCombinators(t *testing.T) {
	mine := movieIn(catalogv1alpha1.MoviePhaseImported)
	mine.Labels = map[string]string{catalogv1alpha1.LabelSource: "bluray"}

	both := And(
		HasLabel(catalogv1alpha1.LabelSource, "bluray"),
		StatusFieldIn(moviePhase, catalogv1alpha1.MoviePhaseImported),
	)
	if !both.Create(event.CreateEvent{Object: mine}) {
		t.Error("And filtered out an object matching both predicates")
	}
	if both.Create(event.CreateEvent{Object: movieIn(catalogv1alpha1.MoviePhaseImported)}) {
		t.Error("And passed an object matching only one predicate")
	}

	either := Or(
		HasLabel(catalogv1alpha1.LabelSource, "webdl"),
		StatusFieldIn(moviePhase, catalogv1alpha1.MoviePhaseImported),
	)
	if !either.Create(event.CreateEvent{Object: movieIn(catalogv1alpha1.MoviePhaseImported)}) {
		t.Error("Or filtered out an object matching one predicate")
	}

	if Not(HasLabel(catalogv1alpha1.LabelSource, "bluray")).Create(event.CreateEvent{Object: mine}) {
		t.Error("Not did not invert")
	}
}
