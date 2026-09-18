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
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
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
	// §10: squasharr's transcodeprofile watches MediaFile and reacts to
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

func downloadPhase(o client.Object) downloadv1alpha1.DownloadPhase {
	d, ok := o.(*downloadv1alpha1.Download)
	if !ok {
		return ""
	}
	return d.Status.Phase
}

func download(phase downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
	d := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "inception-abc1234567", Namespace: "media"},
	}
	d.Status.Phase = phase
	return d
}

func TestStatusFieldInIsTheImporterWatch(t *testing.T) {
	// §10: catalogarr's importer watches Downloads in phase Completed,
	// Seeding or Failed.
	p := StatusFieldIn(downloadPhase,
		downloadv1alpha1.DownloadPhase("Completed"),
		downloadv1alpha1.DownloadPhase("Seeding"),
		downloadv1alpha1.DownloadPhase("Failed"),
	)

	if !p.Create(event.CreateEvent{Object: download("Completed")}) {
		t.Error("a Completed create was filtered out")
	}
	if p.Create(event.CreateEvent{Object: download("Downloading")}) {
		t.Error("a Downloading create passed")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: download("Downloading"), ObjectNew: download("Completed")}) {
		t.Error("the transition into Completed was filtered out")
	}
	if p.Update(event.UpdateEvent{ObjectOld: download("Completed"), ObjectNew: download("Completed")}) {
		t.Error("telemetry churn inside Completed woke the importer")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: download("Completed"), ObjectNew: download("Seeding")}) {
		t.Error("a move between two wanted phases was filtered out")
	}
	if p.Update(event.UpdateEvent{ObjectOld: download("Completed"), ObjectNew: download("Downloading")}) {
		t.Error("a move out of a wanted phase passed")
	}
}

func TestLabelSelector(t *testing.T) {
	// §6.3: an engine replica watches only the Downloads labelled for it.
	sel := metav1.LabelSelector{MatchLabels: map[string]string{
		downloadv1alpha1.LabelEngine: "qbit-0",
	}}
	p, err := LabelSelector(sel)
	if err != nil {
		t.Fatalf("LabelSelector: %v", err)
	}

	mine := download("Downloading")
	mine.Labels = map[string]string{downloadv1alpha1.LabelEngine: "qbit-0"}
	theirs := download("Downloading")
	theirs.Labels = map[string]string{downloadv1alpha1.LabelEngine: "qbit-1"}

	if !p.Create(event.CreateEvent{Object: mine}) {
		t.Error("the engine's own Download was filtered out")
	}
	if p.Create(event.CreateEvent{Object: theirs}) {
		t.Error("a sibling engine's Download passed")
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
		t.Fatal("a malformed selector was accepted; an engine would then watch every Download")
	}
}

func TestHasLabel(t *testing.T) {
	p := HasLabel(downloadv1alpha1.LabelClient, "qbit")
	with := download("Queued")
	with.Labels = map[string]string{downloadv1alpha1.LabelClient: "qbit"}

	if !p.Create(event.CreateEvent{Object: with}) {
		t.Error("a matching object was filtered out")
	}
	if p.Create(event.CreateEvent{Object: download("Queued")}) {
		t.Error("an unlabelled object passed")
	}
}

func TestDeleting(t *testing.T) {
	p := Deleting()
	now := metav1.Now()
	doomed := download("Completed")
	doomed.DeletionTimestamp = &now
	doomed.Finalizers = []string{"download.clustarr.io/download"}

	if !p.Update(event.UpdateEvent{ObjectOld: download("Completed"), ObjectNew: doomed}) {
		t.Error("a deleting object was filtered out")
	}
	if p.Update(event.UpdateEvent{ObjectOld: download("Completed"), ObjectNew: download("Completed")}) {
		t.Error("a live object passed")
	}
}

func TestGenerationChangedIgnoresStatusWrites(t *testing.T) {
	p := GenerationChanged()

	old := download("Downloading")
	old.Generation = 3
	updated := download("Completed")
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
	mine := download("Completed")
	mine.Labels = map[string]string{downloadv1alpha1.LabelClient: "qbit"}

	both := And(
		HasLabel(downloadv1alpha1.LabelClient, "qbit"),
		StatusFieldIn(downloadPhase, downloadv1alpha1.DownloadPhase("Completed")),
	)
	if !both.Create(event.CreateEvent{Object: mine}) {
		t.Error("And filtered out an object matching both predicates")
	}
	if both.Create(event.CreateEvent{Object: download("Completed")}) {
		t.Error("And passed an object matching only one predicate")
	}

	either := Or(
		HasLabel(downloadv1alpha1.LabelClient, "sabnzbd"),
		StatusFieldIn(downloadPhase, downloadv1alpha1.DownloadPhase("Completed")),
	)
	if !either.Create(event.CreateEvent{Object: download("Completed")}) {
		t.Error("Or filtered out an object matching one predicate")
	}

	if Not(HasLabel(downloadv1alpha1.LabelClient, "qbit")).Create(event.CreateEvent{Object: mine}) {
		t.Error("Not did not invert")
	}
}
