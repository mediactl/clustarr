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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The manual-import annotations design spec §8.4 names ("override via
// Download annotations catalog.clustarr.io/import-target=<kind>/<name>[/<key>]
// and catalog.clustarr.io/import-override=true").
//
// They are annotations rather than spec fields because they are a user's
// instruction to the importer, not part of what a Download is: spec.target is
// immutable and is also the Download's ownerReference, so redirecting an
// import must not rewrite it. The same import-target grammar is also honoured
// on a LibraryScan, where it is how a file the scanner left unmatched is
// assigned by hand -- see app/import/worker/rescan's package doc, "Manual
// assignment".
const (
	// AnnotationImportTarget directs an import at one catalog item:
	// "<kind>/<name>" or, for a series or comic, "<kind>/<name>/<key>"
	// where key names the Episode or Issue. See [ParseImportTarget].
	AnnotationImportTarget = "catalog.clustarr.io/import-target"

	// AnnotationImportOverride, set to exactly "true", gives the import the
	// same effect DownloadSpec.Manual has. See [ParseImportOverride] for
	// what that effect is in code, which is narrower than Manual's doc
	// comment says.
	AnnotationImportOverride = "catalog.clustarr.io/import-override"
)

// ImportTarget is a parsed [AnnotationImportTarget] value.
type ImportTarget struct {
	// Kind is the catalog kind the annotation named.
	Kind commonv1.MediaKind

	// Name is the object name in the annotated object's namespace.
	Name string

	// Key is the Episode or Issue name under a series or comic target;
	// empty for every other kind.
	Key string
}

// String renders t back into the annotation grammar.
func (t ImportTarget) String() string {
	if t.Key == "" {
		return string(t.Kind) + "/" + t.Name
	}
	return string(t.Kind) + "/" + t.Name + "/" + t.Key
}

// FileRef is the MediaRef a MediaFile attributed to this target carries: the
// catalog item that holds a file, which for a keyed target is the child, not
// the parent. "comic/saga/saga-00001.0" is a file backing the Issue
// saga-00001.0, and the Issue controller finds its file by
// spec.mediaRef.kind=issue.
func (t ImportTarget) FileRef() commonv1.MediaRef {
	switch {
	case t.Key != "" && t.Kind == commonv1.MediaKindComic:
		return commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: t.Key}
	case t.Key != "" && t.Kind == commonv1.MediaKindSeries:
		return commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: t.Key}
	default:
		return commonv1.MediaRef{Kind: t.Kind, Name: t.Name}
	}
}

// ParseImportTarget parses an [AnnotationImportTarget] value strictly. It
// never repairs a value: whitespace, an unknown kind, a name that is not a
// valid object name, a key on a kind that has no keyed children, or any extra
// segment is an error, because the importer acting on a guessed reading of a
// malformed instruction is exactly the guess the scanner-never-guesses rule
// forbids.
//
// Only series and comic take a key: those are the two kinds whose children
// MediaRef.Keys names ("the Episode or Issue names covered by a pack
// release"). Whether a well-formed target is one this worker can import to
// is a separate question, answered where the target is used.
func ParseImportTarget(value string) (ImportTarget, error) {
	if value == "" {
		return ImportTarget{}, fmt.Errorf("%s is empty; want <kind>/<name>[/<key>]", AnnotationImportTarget)
	}
	if value != strings.TrimSpace(value) {
		return ImportTarget{}, fmt.Errorf("%s %q has leading or trailing whitespace", AnnotationImportTarget, value)
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return ImportTarget{}, fmt.Errorf("%s %q: want <kind>/<name>[/<key>]", AnnotationImportTarget, value)
	}

	t := ImportTarget{Kind: commonv1.MediaKind(parts[0]), Name: parts[1]}
	if !knownKind(t.Kind) {
		return ImportTarget{}, fmt.Errorf("%s %q: unknown kind %q", AnnotationImportTarget, value, parts[0])
	}
	if errs := validation.IsDNS1123Subdomain(t.Name); len(errs) > 0 {
		return ImportTarget{}, fmt.Errorf("%s %q: name %q is not a valid object name: %s",
			AnnotationImportTarget, value, t.Name, strings.Join(errs, "; "))
	}
	if len(parts) == 3 {
		t.Key = parts[2]
		if t.Kind != commonv1.MediaKindSeries && t.Kind != commonv1.MediaKindComic {
			return ImportTarget{}, fmt.Errorf("%s %q: only a series or comic target takes a key; %s has no keyed children",
				AnnotationImportTarget, value, t.Kind)
		}
		if errs := validation.IsDNS1123Subdomain(t.Key); len(errs) > 0 {
			return ImportTarget{}, fmt.Errorf("%s %q: key %q is not a valid object name: %s",
				AnnotationImportTarget, value, t.Key, strings.Join(errs, "; "))
		}
	}
	return t, nil
}

// knownKind reports whether k is one of commonv1.MediaKind's enum values.
func knownKind(k commonv1.MediaKind) bool {
	switch k {
	case commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindEpisode,
		commonv1.MediaKindArtist, commonv1.MediaKindAlbum, commonv1.MediaKindAuthor,
		commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
		commonv1.MediaKindIssue:
		return true
	}
	return false
}

// ParseImportOverride parses an [AnnotationImportOverride] value strictly:
// exactly "true" or "false". strconv.ParseBool's "1", "t" and "TRUE" are
// rejected, so a typo reads as an error on the Download rather than as a
// silently ignored instruction.
//
// The effect of "true" is the effect DownloadSpec.Manual has in this worker,
// and no more: the upgrade decision against an existing file is skipped, a
// transcoded file may be replaced (transcoded.go), an undeterminable
// non-video quality is accepted, a video file only the size floor suspects
// is a sample (Worker.SampleMaxBytes) is imported, and the MediaFile records
// importedFrom.manual -- the list DownloadSpec.Manual's own doc comment
// gives. This worker checks neither monitoring nor availability,
// so neither has anything to skip, and a quality the profile does not allow
// is still rejected under either.
func ParseImportOverride(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s %q: want exactly \"true\" or \"false\"", AnnotationImportOverride, value)
	}
}

// targetFromSpec reads a Download's spec.target in the annotation's terms. A
// series or comic target naming exactly one Episode or Issue in keys is that
// child; any other key count leaves the parent, which the caller refuses as
// a container rather than choose a child for.
func targetFromSpec(ref commonv1.MediaRef) ImportTarget {
	t := ImportTarget{Kind: ref.Kind, Name: ref.Name}
	if len(ref.Keys) == 1 && (ref.Kind == commonv1.MediaKindSeries || ref.Kind == commonv1.MediaKindComic) {
		t.Key = ref.Keys[0]
	}
	return t
}

// directives is what the two annotations on one object say, validated.
type directives struct {
	// target is the annotation's target; nil when the annotation is absent.
	target *ImportTarget

	// override is the parsed import-override value.
	override bool
}

// readDirectives parses both annotations off annotations. A present but
// malformed annotation is an error; an absent one is not.
func readDirectives(annotations map[string]string) (directives, error) {
	var d directives
	if raw, ok := annotations[AnnotationImportTarget]; ok {
		t, err := ParseImportTarget(raw)
		if err != nil {
			return directives{}, err
		}
		d.target = &t
	}
	if raw, ok := annotations[AnnotationImportOverride]; ok {
		v, err := ParseImportOverride(raw)
		if err != nil {
			return directives{}, err
		}
		d.override = v
	}
	return d, nil
}

// FileRefFitsRoot reports whether a file attributed to ref may live under a
// root folder of kind root: a movie under a movie root, an episode under a
// series root, an album under a music root, and so on.
func FileRefFitsRoot(ref commonv1.MediaRef, root catalogv1alpha1.RootFolderKind) bool {
	switch root {
	case catalogv1alpha1.RootFolderKindMovie:
		return ref.Kind == commonv1.MediaKindMovie
	case catalogv1alpha1.RootFolderKindSeries:
		return ref.Kind == commonv1.MediaKindEpisode
	case catalogv1alpha1.RootFolderKindMusic:
		return ref.Kind == commonv1.MediaKindAlbum
	case catalogv1alpha1.RootFolderKindBook:
		return ref.Kind == commonv1.MediaKindBook
	case catalogv1alpha1.RootFolderKindAudiobook:
		return ref.Kind == commonv1.MediaKindAudiobook
	case catalogv1alpha1.RootFolderKindComic:
		return ref.Kind == commonv1.MediaKindIssue
	}
	return false
}
