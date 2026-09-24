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

package actions

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// This file is the Unmatched page's manual-assign action, whose mechanism is
// app/import/worker/rescan/doc.go's "Manual assignment" (carried into this
// task by G2-4, commit 6e1b97b, and the phase plan's G3-4 note). It needs no
// API addition and no new RBAC grant: assigning one unmatched file is a
// create of a LibraryScan, annotated to redirect the walk at one catalog
// item, and the UI's create grant on libraryscans already exists for
// [Rescan]. Annotations carry no RBAC or CRD-schema meaning of their own
// (metadata.annotations is an unvalidated map on every kind), so there is
// nothing else to grant.

// AnnotationImportTarget is app/import/worker/fileimport.AnnotationImportTarget's
// value, restated here for the same reason [FieldManager] restates
// pkg/k8s.ManagerUI's: ui/actions may import only api/ and pkg/obs/ from
// this module (ui/guard_test.go's own import allowlist), and
// app/import/worker/fileimport is neither. manualassign_test.go pins the two
// strings together by importing that package directly (a _test.go file is
// exempt from the allowlist).
const AnnotationImportTarget = "catalog.clustarr.io/import-target"

// ManualAssignTarget is app/import/worker/fileimport.ImportTarget's grammar
// -- "<kind>/<name>" or, for a series or comic, "<kind>/<name>/<key>" --
// restated as a Go struct for the same reason [AnnotationImportTarget] is
// restated as a string: ui/actions cannot import the package that type lives
// in. [ManualAssign] validates a ManualAssignTarget the same way
// fileimport.ParseImportTarget validates a parsed annotation string, so a
// malformed one is refused here, before anything is created, rather than
// only by the worker after the fact.
type ManualAssignTarget struct {
	// Kind is the catalog kind that holds the file: movie, album, book,
	// audiobook, issue, or -- via Key -- series or comic.
	Kind commonv1.MediaKind

	// Name is the target object's name in the LibraryScan's namespace.
	Name string

	// Key names the Episode or Issue under a series or comic target; empty
	// for every other kind. Set only when Kind is series or comic.
	Key string
}

// String renders t into [AnnotationImportTarget]'s wire grammar.
func (t ManualAssignTarget) String() string {
	if t.Key == "" {
		return string(t.Kind) + "/" + t.Name
	}
	return string(t.Kind) + "/" + t.Name + "/" + t.Key
}

// keyedKinds are the only kinds a [ManualAssignTarget] may carry a Key for --
// fileimport.ParseImportTarget's own rule ("only series and comic take a
// key: those are the two kinds whose children MediaRef.Keys names").
var keyedKinds = map[commonv1.MediaKind]bool{
	commonv1.MediaKindSeries: true,
	commonv1.MediaKindComic:  true,
}

// validate checks t's shape exactly as fileimport.ParseImportTarget checks a
// parsed annotation string: a known kind, a valid object name, and a key
// only on a kind whose children have one. It does not check whether t fits
// any particular root folder (fileimport.FileRefFitsRoot) or whether the
// named item exists -- both are the worker's job, reported on the created
// LibraryScan's own status, per this action's own doc comment.
func (t ManualAssignTarget) validate() error {
	if _, ok := monitorables[t.Kind]; !ok {
		return fmt.Errorf("%w: %q is not a catalog media kind (want one of %v)", ErrInvalid, t.Kind, MediaKinds())
	}
	if t.Name == "" || t.Name != strings.TrimSpace(t.Name) {
		return fmt.Errorf("%w: target name is empty or has leading/trailing whitespace (got %q)", ErrInvalid, t.Name)
	}
	if errs := validation.IsDNS1123Subdomain(t.Name); len(errs) > 0 {
		return fmt.Errorf("%w: target name %q is not a valid object name: %s", ErrInvalid, t.Name, strings.Join(errs, "; "))
	}
	if t.Key != "" {
		if !keyedKinds[t.Kind] {
			return fmt.Errorf("%w: only a series or comic target takes a key; %s has no keyed children", ErrInvalid, t.Kind)
		}
		if t.Key != strings.TrimSpace(t.Key) {
			return fmt.Errorf("%w: target key has leading/trailing whitespace (got %q)", ErrInvalid, t.Key)
		}
		if errs := validation.IsDNS1123Subdomain(t.Key); len(errs) > 0 {
			return fmt.Errorf("%w: target key %q is not a valid object name: %s", ErrInvalid, t.Key, strings.Join(errs, "; "))
		}
	}
	return nil
}

// ManualAssign is the Unmatched page's "assign" action
// (app/import/worker/rescan/doc.go, "Manual assignment"): it creates a
// LibraryScan of rootFolder, restricted to subpath (an unmatched file's own
// path, relative to the root -- naming the FILE, not a directory: see that
// doc comment on why LibraryScanSpec.Subpath's "one directory" wording does
// not apply here), annotated with [AnnotationImportTarget]=target.String().
// The worker reads that annotation, walks exactly that one path, and
// attributes it to target without matching -- or refuses it and ends the
// scan Failed with the reason, which is the caller's job to show (this
// action never reads the scan back after creating it).
//
// target is validated before anything is created; a malformed one -- an
// unknown kind, an invalid name, a key on a kind that has none -- returns
// [ErrInvalid] and writes nothing, the same contract [SearchNow] and
// [SetMonitored] already have for their own inputs.
//
// The scan is named "assign-<random>" (generateName), carries
// [LabelOrigin]=[OriginUI] like every other UI-created request, and leaves
// mode as full ([catalogv1alpha1.ScanModeFull]) rather than the CRD's own
// incremental default: a manual assignment names one exact file the scanner
// already looked at and could not attribute, so there is no fingerprint to
// skip re-probing.
func ManualAssign(
	ctx context.Context, c Creator, namespace, rootFolder, subpath string, target ManualAssignTarget,
) (*catalogv1alpha1.LibraryScan, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.ManualAssign")
	defer span.End()

	if namespace == "" || rootFolder == "" || subpath == "" {
		err := fmt.Errorf("%w: manual assign needs a namespace, a RootFolder name and a subpath (got %q/%q/%q)",
			ErrInvalid, namespace, rootFolder, subpath)
		tracing.RecordError(span, err)
		return nil, err
	}
	if err := target.validate(); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "assign-",
			Namespace:    namespace,
			Labels:       map[string]string{LabelOrigin: OriginUI},
			Annotations:  map[string]string{AnnotationImportTarget: target.String()},
		},
		Spec: catalogv1alpha1.LibraryScanSpec{
			RootFolderRef: rootFolder,
			Subpath:       subpath,
			Mode:          catalogv1alpha1.ScanModeFull,
		},
	}
	if err := c.Create(ctx, scan, client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: create manual-assign LibraryScan of %s/%s subpath %q to %s: %w",
			namespace, rootFolder, subpath, target, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: manual assign requested",
		"libraryScan", scan.Name, "namespace", namespace, "rootFolder", rootFolder,
		"subpath", subpath, "target", target.String())
	return scan, nil
}

// ManualAssign is [ManualAssign] over the Actions' writer.
func (a *Actions) ManualAssign(
	ctx context.Context, namespace, rootFolder, subpath string, target ManualAssignTarget,
) (*catalogv1alpha1.LibraryScan, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return ManualAssign(ctx, a.w, namespace, rootFolder, subpath, target)
}
