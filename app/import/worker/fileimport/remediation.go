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

package fileimport

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/quality"
)

// rejection is why the walk refused one file, with the class that decides,
// together with every other file's, what becomes of a download that
// imported nothing (classify). An incidental rejection -- a promo clip
// beside the release, a second file for an item this download already
// filled, a duplicate of a placed file -- explains a file but decides
// nothing while another file does.
type rejection struct {
	class downloadv1alpha1.ImportRejectionClass // "" for incidental
	text  string
	// transcoded marks transcodedRejection's: the item's file is final,
	// which status.import.message says in ImportMessageExistingFileFinal.
	transcoded bool
	// sample marks a suspected sample's, which decides nothing once the
	// walk finds real media beside it (importOutcome.sampleIsIncidental).
	sample bool
}

func (r rejection) none() bool { return r.text == "" }

func transientRejection(format string, args ...any) rejection {
	return rejection{class: downloadv1alpha1.ImportClassTransient, text: fmt.Sprintf(format, args...)}
}

func itemStateRejection(format string, args ...any) rejection {
	return rejection{class: downloadv1alpha1.ImportClassItemState, text: fmt.Sprintf(format, args...)}
}

func needsPersonRejection(format string, args ...any) rejection {
	return rejection{class: downloadv1alpha1.ImportClassNeedsPerson, text: fmt.Sprintf(format, args...)}
}

func releaseFaultRejection(format string, args ...any) rejection {
	return rejection{class: downloadv1alpha1.ImportClassReleaseFault, text: fmt.Sprintf(format, args...)}
}

func incidentalRejection(format string, args ...any) rejection {
	return rejection{text: fmt.Sprintf(format, args...)}
}

// verdictRejection is a file refused as no upgrade. As a rule that is the
// item's state -- its file is as good, or became better after the grab --
// but a file of a worse tier than the quality the release advertised (a
// "2160p" name on a 1080p stream, once probed) is a mislabelled release:
// its fault.
func verdictRejection(p quality.Profile, dl *downloadv1alpha1.Download, actual commonv1.Quality, text string) rejection {
	advertised := dl.Spec.Release.Quality
	if advertised.Name != "" {
		want, okWant := p.Index(advertised)
		got, okGot := p.Index(actual)
		if okWant && okGot && got > want {
			return releaseFaultRejection("%s; the release advertised %s, the file is %s", text, advertised.Name, actual.Name)
		}
	}
	return itemStateRejection("%s", text)
}

// classOfErr is the class of an import stopped by err: errBlocked (a
// placement outside the root folder, a path that cannot be rendered, a
// target that holds no files) needs a person, and anything else -- the
// apiserver, the disk, the NAS -- is transient.
func classOfErr(err error) downloadv1alpha1.ImportRejectionClass {
	if errors.Is(err, errBlocked) {
		return downloadv1alpha1.ImportClassNeedsPerson
	}
	return downloadv1alpha1.ImportClassTransient
}

// texts renders rs for status.import.rejections.
func texts(rs []rejection) []string {
	if len(rs) == 0 {
		return nil
	}
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.text
	}
	return out
}

// classPrecedence orders the classes for classify: any doubt holds a
// download, and only a release whose every decisive file is at fault is
// condemned.
var classPrecedence = []downloadv1alpha1.ImportRejectionClass{
	downloadv1alpha1.ImportClassTransient,
	downloadv1alpha1.ImportClassNeedsPerson,
	downloadv1alpha1.ImportClassItemState,
	downloadv1alpha1.ImportClassReleaseFault,
}

// classify is the class of an import that imported nothing, from every
// rejection it made: the first of classPrecedence any file was refused for.
// A download that offered no decisive file at all -- nothing walked, or
// only incidental files such as a promo clip -- held no media: the release
// is at fault.
func classify(rs []rejection) downloadv1alpha1.ImportRejectionClass {
	seen := map[downloadv1alpha1.ImportRejectionClass]bool{}
	for _, r := range rs {
		seen[r.class] = true
	}
	for _, c := range classPrecedence {
		if seen[c] {
			return c
		}
	}
	return downloadv1alpha1.ImportClassReleaseFault
}

// outcomeMessage is status.import.message for a walk that imported nothing,
// before conclude says what happens next. A walk with no rejection at all
// says so in noneMessage ("no importable files found").
func outcomeMessage(rs []rejection, noneMessage string) string {
	if len(rs) == 0 {
		return noneMessage
	}
	switch classify(rs) {
	case downloadv1alpha1.ImportClassTransient:
		return "a file could not be read, probed or placed"
	case downloadv1alpha1.ImportClassNeedsPerson:
		return fmt.Sprintf("a file cannot be attributed or placed without a person: import it by hand "+
			"(spec.manual, or %s=true, with %s to name the item)", AnnotationImportOverride, AnnotationImportTarget)
	case downloadv1alpha1.ImportClassItemState:
		for _, r := range rs {
			if r.transcoded {
				return downloadv1alpha1.ImportMessageExistingFileFinal
			}
		}
		return "the item does not take these files now"
	default:
		return downloadv1alpha1.ImportMessageEveryFileRejected
	}
}

// conclude settles an import that imported nothing, by its class -- the
// remediation the owner set on 2026-10-07 in place of blocklisting every
// refused release at once, which threw away a good 20 GB download the item
// merely would not take:
//
//   - transient: walked again on the import consumer's backoff (1m, 10m,
//     45m), status.import pending with nextAttemptAt meanwhile; after the
//     last delivery, held.
//   - releaseFault: walked once more to confirm, on the first backoff
//     step; refused as the release's fault again, blocked, which grabarr
//     blocklists before searching again.
//   - itemState, needsPerson: held at once.
//
// A held import keeps its files for downloadv1alpha1.ImportHoldRetention;
// grabarr then fails the Download as importExpired and its engine removes
// them, without blocklisting the release.
func (w *Worker) conclude(
	ctx context.Context, m events.Message, dl *downloadv1alpha1.Download, class downloadv1alpha1.ImportRejectionClass,
	imported []*downloadac.ImportedFileApplyConfiguration, rejections []string, message string,
) error {
	now := w.now()
	attempt := m.Attempt()
	retry := !w.finalAttempt(m) && (class == downloadv1alpha1.ImportClassTransient ||
		(class == downloadv1alpha1.ImportClassReleaseFault && attempt < 2))
	if retry {
		next := now.Add(retryDelay(attempt))
		note := "retrying"
		if class == downloadv1alpha1.ImportClassReleaseFault {
			note = "walking it once more before the release is blocklisted"
		}
		ac := importState(downloadv1alpha1.ImportPhasePending, class, attempt, imported, rejections,
			fmt.Sprintf("%s; %s at %s (attempt %d of %d)", message, note, next.UTC().Format(time.RFC3339), attempt+1, maxDeliver())).
			WithNextAttemptAt(metav1.NewTime(next))
		if err := w.patchImport(ctx, dl, ac, nil); err != nil {
			return err
		}
		return events.Retry(0, fmt.Errorf("fileimport: %s: %s", class, message))
	}
	if class == downloadv1alpha1.ImportClassReleaseFault {
		return w.patchImport(ctx, dl, importState(downloadv1alpha1.ImportPhaseBlocked, class, attempt, imported, rejections, message), nil)
	}
	until := now.Add(downloadv1alpha1.ImportHoldRetention)
	ac := importState(downloadv1alpha1.ImportPhaseBlocked, class, attempt, imported, rejections,
		fmt.Sprintf("%s; held for a person until %s, then its files are removed", message, until.UTC().Format(time.RFC3339))).
		WithHeldSince(metav1.NewTime(now))
	return w.patchImport(ctx, dl, ac, nil)
}

// importState renders status.import for conclude.
func importState(
	state downloadv1alpha1.ImportPhase, class downloadv1alpha1.ImportRejectionClass, attempt uint64,
	imported []*downloadac.ImportedFileApplyConfiguration, rejections []string, message string,
) *downloadac.ImportStateApplyConfiguration {
	ac := downloadac.ImportState().
		WithState(state).
		WithClass(class).
		WithAttempts(int32(min(attempt, 1000))). //nolint:gosec // bounded above.
		WithMessage(truncateChars(message, maxImportMessage))
	if listed, _ := capImported(imported); len(listed) > 0 {
		ac = ac.WithImported(listed...)
	}
	if len(rejections) > 0 {
		ac = ac.WithRejections(capRejections(rejections)...)
	}
	return ac
}

// retryDelay is how long the import consumer's backoff waits after delivery
// attempt before the next: what status.import.nextAttemptAt reports.
func retryDelay(attempt uint64) time.Duration {
	spec, ok := events.Default().Consumer(events.ConsumerImportFile)
	if !ok || len(spec.BackOff) == 0 {
		return spec.AckWait
	}
	i := int(min(attempt, uint64(len(spec.BackOff)))) - 1 //nolint:gosec // bounded by len(BackOff).
	return spec.BackOff[max(i, 0)]
}

// maxDeliver is how many times the import consumer delivers one task.
func maxDeliver() int {
	spec, _ := events.Default().Consumer(events.ConsumerImportFile)
	return spec.MaxDeliver
}
