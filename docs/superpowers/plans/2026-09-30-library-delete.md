# Library Delete Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Delete button on every library item (Movie, Series, Artist, Author, Audiobook, Comic) that removes the item and its MediaFile records, optionally its folder on disk (recursively, permanently, pruning emptied parents), and optionally adds an ImportExclusion the library scan now honours.

**Architecture:** The ui writes two annotations with a merge patch (it already holds `patch` on catalog kinds). A new importarr controller, `app/import/controller/librarydelete`, sees the annotation, checks every path is safe, removes the folder with `fsops.SafeRemove`, deletes the MediaFiles and then the item. The rescan worker checks the `clustarr-import-exclusions` bucket before auto-creating a Movie or Series.

**Tech Stack:** Go, controller-runtime (fake client in unit tests), templ + htmx ui, shadcn-templ `dialog` and `checkbox` components.

**Spec:** `docs/superpowers/specs/2026-09-30-library-delete-design.md`

## Global Constraints

- Annotation `catalog.clustarr.io/delete`: value `files` or `records`; any other value is refused.
- Annotation `catalog.clustarr.io/delete-add-exclusion`: `true`.
- Annotation `catalog.clustarr.io/delete-error`: the refusal or failure reason, written by importarr.
- Removal is permanent: no recycle bin, even when the RootFolder configures one.
- Never remove the RootFolder itself; prune stops below it.
- Refuse (remove nothing) when any path is not strictly under the RootFolder's `spec.path`, or the item folder holds another item's MediaFile.
- The ui never deletes and gains no grant; `TestUINeverWrites` and the ui role test stay green unchanged.
- GPL-3.0 header from `hack/boilerplate.go.txt` on every new Go file.
- Logging via `logging.FromContext`; no package-level logger.
- Unit gate only: `env -u KUBEBUILDER_ASSETS -u CLUSTARR_PG_ASSETS go test ./...` plus `make lint` (envtest is skipped by the owner's instruction).
- Commit with a pathspec: `git commit -m '...' -- <paths>`; never `git stash`; never push from a task.
- `make build`, never bare `go build`.

## Review Focus

1. **A folder that does not exist on disk** (never created, already removed by a crashed earlier run) -- the delete must complete, not fail on `EvalSymlinks`. Pinned in Task 2 (`TestRemoveFromDiskSkipsWhatIsAlreadyGone`).
2. **An item whose `status.path` is empty** (metadata never resolved) with `files` -- remove only its MediaFiles' paths, never guess a folder. Pinned in Task 2 (`TestCheckWithNoFolderRemovesOnlyTheFiles`).
3. **A second delete request while the first is running, or the item's own `delete-error` write** -- the predicate must not loop on importarr's own annotation write. Pinned in Task 3 (`TestDeleteRequestedPredicate`).
4. **A Series with a multi-episode MediaFile** (pack `keys`) -- the file belongs to the series through any episode in its keys, so it is deleted and does not count as "another item's". Pinned in Task 2 (`TestCheckTreatsAPackFileAsTheSeries`).
5. **The folder itself is a symlink** -- the link is removed, its target's contents are untouched. Pinned in Task 2 (`TestRemoveFromDiskDoesNotFollowASymlinkedFolder`).

---

### Task 1: The annotations, the exclusion mapping, and shared path helpers

**Files:**
- Modify: `api/catalog/v1alpha1/shared_types.go` (add constants after `AnnotationRefreshMetadata`, line ~63)
- Create: `api/catalog/v1alpha1/delete.go`
- Create: `api/catalog/v1alpha1/delete_test.go`
- Create: `pkg/fsops/under.go`
- Create: `pkg/fsops/under_test.go`
- Modify: `app/import/worker/importlist/delete.go` (use the fsops helpers; delete its private `strictlyUnder` and `pruneEmptyDirs`)

**Interfaces:**
- Produces:
  - `catalogv1alpha1.AnnotationDelete = "catalog.clustarr.io/delete"`, `AnnotationDeleteAddExclusion = "catalog.clustarr.io/delete-add-exclusion"`, `AnnotationDeleteError = "catalog.clustarr.io/delete-error"`, `DeleteFiles = "files"`, `DeleteRecords = "records"`.
  - `func catalogv1alpha1.DeletableKinds() []commonv1.MediaKind` -- Movie, Series, Artist, Author, Audiobook, Comic.
  - `func catalogv1alpha1.ExclusionKindFor(kind commonv1.MediaKind) (ExclusionKind, bool)` -- movie, series, audiobook, comic; false for every other kind.
  - `func fsops.StrictlyUnder(path, root string) bool`
  - `func fsops.PruneEmptyDirs(root, dir string)`

- [ ] **Step 1: Write the failing tests**

`api/catalog/v1alpha1/delete_test.go`:

```go
package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestExclusionKindFor(t *testing.T) {
	for kind, want := range map[commonv1.MediaKind]catalogv1alpha1.ExclusionKind{
		commonv1.MediaKindMovie:     catalogv1alpha1.ExclusionKindMovie,
		commonv1.MediaKindSeries:    catalogv1alpha1.ExclusionKindSeries,
		commonv1.MediaKindAudiobook: catalogv1alpha1.ExclusionKindAudiobook,
		commonv1.MediaKindComic:     catalogv1alpha1.ExclusionKindComic,
	} {
		got, ok := catalogv1alpha1.ExclusionKindFor(kind)
		assert.True(t, ok, kind)
		assert.Equal(t, want, got, kind)
	}
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindArtist, commonv1.MediaKindAuthor, commonv1.MediaKindEpisode} {
		_, ok := catalogv1alpha1.ExclusionKindFor(kind)
		assert.False(t, ok, kind)
	}
}

func TestDeletableKinds(t *testing.T) {
	assert.ElementsMatch(t, []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist,
		commonv1.MediaKindAuthor, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
	}, catalogv1alpha1.DeletableKinds())
}
```

`pkg/fsops/under_test.go`:

```go
package fsops_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestStrictlyUnder(t *testing.T) {
	assert.True(t, fsops.StrictlyUnder("/data/media/tv/Andor", "/data/media/tv"))
	assert.False(t, fsops.StrictlyUnder("/data/media/tv", "/data/media/tv"), "the root itself")
	assert.False(t, fsops.StrictlyUnder("/data/media/tv2/x", "/data/media/tv"), "a sibling with a shared prefix")
	assert.False(t, fsops.StrictlyUnder("/data/media/tv/../movies/x", "/data/media/tv"))
	assert.False(t, fsops.StrictlyUnder("tv/Andor", "/data/media"), "relative")
}

func TestPruneEmptyDirsStopsBelowTheRoot(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "keep.txt"), []byte("x"), 0o644))
	fsops.PruneEmptyDirs(root, deep)
	assert.NoDirExists(t, filepath.Join(root, "a", "b"))
	assert.DirExists(t, filepath.Join(root, "a"), "not empty")
	assert.DirExists(t, root)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./api/catalog/v1alpha1/ ./pkg/fsops/ -run 'ExclusionKindFor|DeletableKinds|StrictlyUnder|PruneEmptyDirs'`
Expected: build failure, `undefined: catalogv1alpha1.ExclusionKindFor` and `undefined: fsops.StrictlyUnder`.

- [ ] **Step 3: Implement**

Append to `api/catalog/v1alpha1/shared_types.go`:

```go
// The library delete request (docs/superpowers/specs/
// 2026-09-30-library-delete-design.md). The ui, or kubectl, sets
// AnnotationDelete on a library item; importarr's librarydelete controller
// carries it out and deletes the item, or writes AnnotationDeleteError.
const (
	// AnnotationDelete asks for the item to be deleted: DeleteRecords
	// removes the item and its MediaFile records, DeleteFiles its folder
	// and files on disk as well, permanently.
	AnnotationDelete = "catalog.clustarr.io/delete"
	// AnnotationDeleteAddExclusion, "true", also creates an ImportExclusion
	// for the item before it goes.
	AnnotationDeleteAddExclusion = "catalog.clustarr.io/delete-add-exclusion"
	// AnnotationDeleteError is why a delete was refused or failed; importarr
	// writes it, and a new request clears it.
	AnnotationDeleteError = "catalog.clustarr.io/delete-error"

	// DeleteFiles and DeleteRecords are AnnotationDelete's two values.
	DeleteFiles   = "files"
	DeleteRecords = "records"
)
```

`api/catalog/v1alpha1/delete.go` (GPL header first):

```go
package v1alpha1

import commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"

// DeletableKinds are the kinds the library delete acts on: every kind with a
// library card and a folder of its own. Episode, Album, Book and Issue go
// with their parent.
func DeletableKinds() []commonv1.MediaKind {
	return []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist,
		commonv1.MediaKindAuthor, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
	}
}

// ExclusionKindFor is the ImportExclusion kind that keeps a deleted item of
// kind out, and false for a kind with none (Artist, Author, and every child
// kind): the delete dialog offers "Add import list exclusion" only for these.
func ExclusionKindFor(kind commonv1.MediaKind) (ExclusionKind, bool) {
	switch kind {
	case commonv1.MediaKindMovie:
		return ExclusionKindMovie, true
	case commonv1.MediaKindSeries:
		return ExclusionKindSeries, true
	case commonv1.MediaKindAudiobook:
		return ExclusionKindAudiobook, true
	case commonv1.MediaKindComic:
		return ExclusionKindComic, true
	default:
		return "", false
	}
}
```

`pkg/fsops/under.go` (GPL header first) -- moved verbatim from `app/import/worker/importlist/delete.go`, exported:

```go
package fsops

import (
	"os"
	"path/filepath"
	"strings"
)

// StrictlyUnder reports whether path names something inside root, and not
// root itself, once both are cleaned. Both must be absolute.
func StrictlyUnder(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PruneEmptyDirs removes dir and then each parent while it is empty,
// stopping at the first directory that is not empty (os.Remove refuses
// one) and never removing root or anything outside it.
func PruneEmptyDirs(root, dir string) {
	for StrictlyUnder(dir, root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
```

In `app/import/worker/importlist/delete.go`, delete the functions `strictlyUnder` and `pruneEmptyDirs`, and replace their calls with `fsops.StrictlyUnder` and `fsops.PruneEmptyDirs` (the file already imports `pkg/fsops`; drop the `strings` import if unused).

- [ ] **Step 4: Run the tests**

Run: `go test ./api/catalog/v1alpha1/ ./pkg/fsops/ ./app/import/worker/importlist/`
Expected: PASS (the importlist tests prove the move changed nothing).

- [ ] **Step 5: Commit**

```bash
git add api/catalog/v1alpha1/delete.go api/catalog/v1alpha1/delete_test.go pkg/fsops/under.go pkg/fsops/under_test.go
git commit -m "feat(api): the library delete annotations, DeletableKinds and ExclusionKindFor; fsops.StrictlyUnder and PruneEmptyDirs shared from importlist" -- api/catalog/v1alpha1/shared_types.go api/catalog/v1alpha1/delete.go api/catalog/v1alpha1/delete_test.go pkg/fsops/under.go pkg/fsops/under_test.go app/import/worker/importlist/delete.go
```

---

### Task 2: The delete's core -- targets, safety checks, disk removal

**Files:**
- Create: `app/import/controller/librarydelete/target.go`
- Create: `app/import/controller/librarydelete/target_test.go`

**Interfaces:**
- Consumes: `fsops.StrictlyUnder`, `fsops.PruneEmptyDirs`, `fsops.SafeRemove(ctx, root, path string) error`.
- Produces:
  - `type Target struct { Root, Folder string; Keys map[string]bool; Files []catalogv1alpha1.MediaFile }` -- `Keys` holds `"<kind>/<name>"` for the item and each child.
  - `var ErrRefused = errors.New("librarydelete: refused")`
  - `func (t Target) Owns(ref commonv1.MediaRef) bool`
  - `func Check(t Target, all []catalogv1alpha1.MediaFile) error` -- wraps `ErrRefused`.
  - `func RemoveFromDisk(ctx context.Context, t Target) error`
  - `func TargetKey(kind commonv1.MediaKind, name string) string` -- `string(kind) + "/" + name`.

- [ ] **Step 1: Write the failing tests** (`target_test.go`, package `librarydelete`)

```go
package librarydelete

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func mediaFile(name string, ref commonv1.MediaRef, path string, sidecars ...string) catalogv1alpha1.MediaFile {
	mf := catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: path},
	}
	for _, s := range sidecars {
		mf.Status.Sidecars = append(mf.Status.Sidecars, catalogv1alpha1.Sidecar{Path: s})
	}
	return mf
}

func write(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
}

var heat = commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}

func heatTarget(root string) (Target, catalogv1alpha1.MediaFile) {
	folder := filepath.Join(root, "Heat (1995)")
	mf := mediaFile("heat-file", heat, filepath.Join(folder, "Heat.mkv"), filepath.Join(folder, "Heat.en.srt"))
	return Target{
		Root: root, Folder: folder,
		Keys:  map[string]bool{TargetKey(commonv1.MediaKindMovie, "heat"): true},
		Files: []catalogv1alpha1.MediaFile{mf},
	}, mf
}

// The whole folder goes, untracked files included, and its emptied parent
// with it; the RootFolder stays.
func TestRemoveFromDiskRemovesTheFolderAndPrunes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "movies")
	tgt, _ := heatTarget(filepath.Join(root, "H"))
	tgt.Root = root
	write(t, filepath.Join(tgt.Folder, "Heat.mkv"))
	write(t, filepath.Join(tgt.Folder, "Heat.en.srt"))
	write(t, filepath.Join(tgt.Folder, "movie.nfo"))
	write(t, filepath.Join(tgt.Folder, "Extras", "trailer.mkv"))

	require.NoError(t, Check(tgt, tgt.Files))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	assert.NoDirExists(t, tgt.Folder)
	assert.NoDirExists(t, filepath.Join(root, "H"), "emptied parent pruned")
	assert.DirExists(t, root, "the RootFolder is never removed")
}

func TestCheckRefusesAFolderHoldingAnotherItemsFile(t *testing.T) {
	root := t.TempDir()
	tgt, mf := heatTarget(root)
	other := mediaFile("ronin-file", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "ronin"},
		filepath.Join(tgt.Folder, "Ronin.mkv"))
	err := Check(tgt, []catalogv1alpha1.MediaFile{mf, other})
	require.ErrorIs(t, err, ErrRefused)
	assert.Contains(t, err.Error(), "ronin-file")
}

func TestCheckRefusesAPathOutsideTheRootFolder(t *testing.T) {
	root := t.TempDir()
	tgt, _ := heatTarget(root)
	tgt.Files[0].Status.Sidecars = append(tgt.Files[0].Status.Sidecars, catalogv1alpha1.Sidecar{Path: "/etc/passwd"})
	require.ErrorIs(t, Check(tgt, tgt.Files), ErrRefused)

	tgt, _ = heatTarget(root)
	tgt.Folder = root
	require.ErrorIs(t, Check(tgt, tgt.Files), ErrRefused, "the RootFolder itself")
}

func TestCheckRefusesAnUnknownRootFolder(t *testing.T) {
	tgt, _ := heatTarget(t.TempDir())
	tgt.Root = ""
	require.ErrorIs(t, Check(tgt, tgt.Files), ErrRefused)
}

// A multi-episode file names one episode and lists the others in keys: any
// of them belonging to the series makes it the series' file.
func TestCheckTreatsAPackFileAsTheSeries(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Andor")
	pack := mediaFile("pack", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e01",
		Keys: []string{"andor-s01e01", "andor-s01e02"}}, filepath.Join(folder, "S01E01-E02.mkv"))
	tgt := Target{Root: root, Folder: folder, Keys: map[string]bool{
		TargetKey(commonv1.MediaKindSeries, "andor"):   true,
		TargetKey(commonv1.MediaKindEpisode, "andor-s01e02"): true,
	}}
	assert.True(t, tgt.Owns(pack.Spec.MediaRef))
	require.NoError(t, Check(tgt, []catalogv1alpha1.MediaFile{pack}))
}

func TestCheckWithNoFolderRemovesOnlyTheFiles(t *testing.T) {
	root := t.TempDir()
	tgt, _ := heatTarget(root)
	tgt.Folder = ""
	write(t, tgt.Files[0].Spec.Path)
	write(t, filepath.Join(filepath.Dir(tgt.Files[0].Spec.Path), "movie.nfo"))
	require.NoError(t, Check(tgt, tgt.Files))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	assert.NoFileExists(t, tgt.Files[0].Spec.Path)
	assert.FileExists(t, filepath.Join(filepath.Dir(tgt.Files[0].Spec.Path), "movie.nfo"),
		"with no folder resolved nothing is removed but the recorded files")
}

func TestRemoveFromDiskSkipsWhatIsAlreadyGone(t *testing.T) {
	tgt, _ := heatTarget(t.TempDir())
	require.NoError(t, RemoveFromDisk(context.Background(), tgt), "a retry after a partial run")
}

func TestRemoveFromDiskDoesNotFollowASymlinkedFolder(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "precious.mkv"))
	tgt, _ := heatTarget(root)
	require.NoError(t, os.Symlink(elsewhere, tgt.Folder))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	_, err := os.Lstat(tgt.Folder)
	assert.True(t, errors.Is(err, os.ErrNotExist), "the link is gone")
	assert.FileExists(t, filepath.Join(elsewhere, "precious.mkv"), "its target is untouched")
}

// A file recorded outside the folder (a sidecar next to it, an old path)
// is removed on its own.
func TestRemoveFromDiskRemovesFilesOutsideTheFolder(t *testing.T) {
	root := t.TempDir()
	tgt, _ := heatTarget(root)
	stray := filepath.Join(root, "loose", "Heat.old.mkv")
	tgt.Files = append(tgt.Files, mediaFile("heat-old", heat, stray))
	write(t, stray)
	require.NoError(t, Check(tgt, tgt.Files))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	assert.NoFileExists(t, stray)
	assert.NoDirExists(t, filepath.Join(root, "loose"))
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./app/import/controller/librarydelete/`
Expected: build failure, `undefined: Target`.

- [ ] **Step 3: Implement** (`target.go`, GPL header first)

```go
// Package librarydelete carries out a library delete request: an item
// annotated catalog.clustarr.io/delete is removed with its MediaFile
// records and, for "files", its folder on disk (docs/superpowers/specs/
// 2026-09-30-library-delete-design.md).
package librarydelete

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
)

// ErrRefused is a delete that would reach outside the item: nothing is
// removed, and the reason goes on the item until the request is renewed.
var ErrRefused = errors.New("librarydelete: refused")

// Target is everything one delete acts on.
type Target struct {
	// Root is the item's RootFolder spec.path; "" when it has none.
	Root string
	// Folder is the item's status.path; "" when never resolved.
	Folder string
	// Keys are TargetKey of the item and of each of its children.
	Keys map[string]bool
	// Files are the MediaFiles of the item and its children.
	Files []catalogv1alpha1.MediaFile
}

// TargetKey is the "<kind>/<name>" a MediaRef names.
func TargetKey(kind commonv1.MediaKind, name string) string { return string(kind) + "/" + name }

// Owns reports whether a MediaFile with ref belongs to the item: it names
// the item or a child, or -- a multi-episode file -- lists one in keys.
func (t Target) Owns(ref commonv1.MediaRef) bool {
	if t.Keys[TargetKey(ref.Kind, ref.Name)] {
		return true
	}
	return slices.ContainsFunc(ref.Keys, func(k string) bool { return t.Keys[TargetKey(ref.Kind, k)] })
}

// paths is every file path and sidecar the target's MediaFiles record.
func (t Target) paths() []string {
	var out []string
	for _, mf := range t.Files {
		out = append(out, mf.Spec.Path)
		for _, sc := range mf.Status.Sidecars {
			out = append(out, sc.Path)
		}
	}
	return out
}

// Check refuses, wrapping ErrRefused, a delete of files that would reach
// outside the item: no RootFolder, a folder or path not strictly under it,
// or a folder holding a MediaFile of another item (all is every MediaFile
// in the namespace).
func Check(t Target, all []catalogv1alpha1.MediaFile) error {
	if t.Root == "" {
		return fmt.Errorf("%w: the item's root folder is unknown", ErrRefused)
	}
	if t.Folder != "" && !fsops.StrictlyUnder(t.Folder, t.Root) {
		return fmt.Errorf("%w: folder %q is not inside root folder %q", ErrRefused, t.Folder, t.Root)
	}
	for _, p := range t.paths() {
		if !fsops.StrictlyUnder(p, t.Root) {
			return fmt.Errorf("%w: file %q is not inside root folder %q", ErrRefused, p, t.Root)
		}
	}
	if t.Folder == "" {
		return nil
	}
	for i := range all {
		mf := &all[i]
		if t.Owns(mf.Spec.MediaRef) {
			continue
		}
		if fsops.StrictlyUnder(mf.Spec.Path, t.Folder) {
			return fmt.Errorf("%w: folder %q also holds media file %s of %s %s",
				ErrRefused, t.Folder, mf.Name, mf.Spec.MediaRef.Kind, mf.Spec.MediaRef.Name)
		}
	}
	return nil
}

// RemoveFromDisk removes the folder, recursively, and each recorded path
// outside it, permanently, then prunes the directories that leaves empty up
// to (never including) the root. A path already gone is done, so a retry
// after a partial run completes. Symlinks are never followed: a symlinked
// folder loses the link, not its target's contents (fsops.SafeRemove).
func RemoveFromDisk(ctx context.Context, t Target) error {
	var removed []string
	remove := func(p string) error {
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			removed = append(removed, p)
			return nil
		}
		if err := fsops.SafeRemove(ctx, t.Root, p); err != nil {
			return err
		}
		removed = append(removed, p)
		return nil
	}
	if t.Folder != "" {
		if err := remove(t.Folder); err != nil {
			return err
		}
	}
	for _, p := range t.paths() {
		if t.Folder != "" && fsops.StrictlyUnder(p, t.Folder) {
			continue
		}
		if err := remove(p); err != nil {
			return err
		}
	}
	for _, p := range removed {
		fsops.PruneEmptyDirs(t.Root, filepath.Dir(p))
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./app/import/controller/librarydelete/`
Expected: PASS, all nine.

- [ ] **Step 5: Commit**

```bash
git add app/import/controller/librarydelete/target.go app/import/controller/librarydelete/target_test.go
git commit -m "feat(import): librarydelete's core -- a delete's targets, the checks that refuse one reaching outside the item, and permanent removal of its folder with emptied parents pruned" -- app/import/controller/librarydelete/
```

---

### Task 3: The librarydelete controller, wired into importarr

**Files:**
- Create: `app/import/controller/librarydelete/kinds.go`
- Create: `app/import/controller/librarydelete/reconciler.go`
- Create: `app/import/controller/librarydelete/reconciler_test.go`
- Modify: `app/import/run.go` (`setupControllers`, after the importexclusion reconciler)
- Regenerate: `config/rbac/importarr_role.yaml` and the chart copy (`make manifests`; the chart's rbac.yaml block between the sentinels is held byte-identical by a test -- copy the regenerated importarr rules in)

**Interfaces:**
- Consumes: Task 1 constants, `ExclusionKindFor`; Task 2 `Target`, `Check`, `RemoveFromDisk`, `ErrRefused`, `TargetKey`.
- Produces:
  - `func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error`
  - `type Reconciler struct { client.Client; Recorder k8sevents.EventRecorder; kind kindSpec }`
  - `func SetupWithManager(mgr ctrl.Manager) error` -- registers the indexes once and one controller per deletable kind, named `librarydelete-<kind>`.
  - `func deleteRequested() predicate.Predicate`

- [ ] **Step 1: Write the failing tests** (`reconciler_test.go`, package `librarydelete`)

```go
package librarydelete

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

type builderIndexer struct{ b *fake.ClientBuilder }

func (i builderIndexer) IndexField(_ context.Context, obj client.Object, field string, fn client.IndexerFunc) error {
	i.b.WithIndex(obj, field, fn)
	return nil
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...)
	require.NoError(t, RegisterIndexes(context.Background(), builderIndexer{b}))
	return b.Build()
}

func rootFolder(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "movies"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindMovie},
	}
}

func heatMovie(folder, mode string, exclude bool) *catalogv1alpha1.Movie {
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDelete: mode}},
		Spec:   catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies"},
		Status: catalogv1alpha1.MovieStatus{Path: folder},
	}
	if exclude {
		m.Annotations[catalogv1alpha1.AnnotationDeleteAddExclusion] = "true"
	}
	return m
}

func reconcileMovie(t *testing.T, c client.Client) error {
	t.Helper()
	r := &Reconciler{Client: c, kind: kindFor(commonv1.MediaKindMovie)}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: "heat"}})
	return err
}

func TestDeleteFilesRemovesFolderRecordsAndItem(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Heat (1995)")
	write(t, filepath.Join(folder, "Heat.mkv"))
	write(t, filepath.Join(folder, "movie.nfo"))
	mf := mediaFile("heat-file", heat, filepath.Join(folder, "Heat.mkv"))
	c := newClient(t, rootFolder(root), heatMovie(folder, catalogv1alpha1.DeleteFiles, true), &mf)

	require.NoError(t, reconcileMovie(t, c))

	assert.NoDirExists(t, folder)
	assert.DirExists(t, root)
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(&mf), &catalogv1alpha1.MediaFile{})))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &catalogv1alpha1.Movie{})))
	var ex catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &ex))
	assert.Equal(t, catalogv1alpha1.ExclusionKindMovie, ex.Spec.Kind)
	assert.Equal(t, map[string]string{catalogv1alpha1.ExclusionIDKeyTMDB: "949"}, ex.Spec.ExternalIDs)
}

func TestDeleteRecordsLeavesTheDiskAlone(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Heat (1995)")
	write(t, filepath.Join(folder, "Heat.mkv"))
	mf := mediaFile("heat-file", heat, filepath.Join(folder, "Heat.mkv"))
	c := newClient(t, rootFolder(root), heatMovie(folder, catalogv1alpha1.DeleteRecords, false), &mf)

	require.NoError(t, reconcileMovie(t, c))

	assert.FileExists(t, filepath.Join(folder, "Heat.mkv"))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(&mf), &catalogv1alpha1.MediaFile{})))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &catalogv1alpha1.Movie{})))
	var list catalogv1alpha1.ImportExclusionList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Empty(t, list.Items)
}

func TestARefusalRemovesNothingAndSaysWhy(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Heat (1995)")
	write(t, filepath.Join(folder, "Heat.mkv"))
	write(t, filepath.Join(folder, "Ronin.mkv"))
	mine := mediaFile("heat-file", heat, filepath.Join(folder, "Heat.mkv"))
	other := mediaFile("ronin-file", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "ronin"}, filepath.Join(folder, "Ronin.mkv"))
	c := newClient(t, rootFolder(root), heatMovie(folder, catalogv1alpha1.DeleteFiles, false), &mine, &other)

	require.NoError(t, reconcileMovie(t, c), "a refusal is not retried")

	assert.FileExists(t, filepath.Join(folder, "Heat.mkv"))
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(&mine), &catalogv1alpha1.MediaFile{}))
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &m))
	assert.Contains(t, m.Annotations[catalogv1alpha1.AnnotationDeleteError], "ronin-file")
	assert.Equal(t, catalogv1alpha1.DeleteFiles, m.Annotations[catalogv1alpha1.AnnotationDelete], "the request stays")
}

func TestAnUnknownValueIsRefused(t *testing.T) {
	root := t.TempDir()
	c := newClient(t, rootFolder(root), heatMovie(filepath.Join(root, "Heat"), "everything", false))
	require.NoError(t, reconcileMovie(t, c))
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "heat"}, &m))
	assert.Contains(t, m.Annotations[catalogv1alpha1.AnnotationDeleteError], "everything")
}

// A Series takes its Episodes' MediaFiles with it, found through the
// episode index, and leaves another series' files alone.
func TestDeleteSeriesTakesItsEpisodesFiles(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Andor")
	write(t, filepath.Join(folder, "S01E01.mkv"))
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "andor",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDelete: catalogv1alpha1.DeleteFiles}},
		Spec:   catalogv1alpha1.SeriesSpec{TvdbID: 1, QualityProfileRef: "hd", RootFolderRef: "movies"},
		Status: catalogv1alpha1.SeriesStatus{Path: folder},
	}
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "andor-s01e01"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: 1, EpisodeNumber: 1},
	}
	mf := mediaFile("andor-file", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e01"}, filepath.Join(folder, "S01E01.mkv"))
	keep := mediaFile("other-file", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "other-s01e01"}, filepath.Join(root, "Other", "S01E01.mkv"))
	c := newClient(t, rootFolder(root), s, ep, &mf, &keep)

	r := &Reconciler{Client: c, kind: kindFor(commonv1.MediaKindSeries)}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: "andor"}})
	require.NoError(t, err)
	assert.NoDirExists(t, folder)
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(&mf), &catalogv1alpha1.MediaFile{})))
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(&keep), &catalogv1alpha1.MediaFile{}))
}

// A retry finds the exclusion it made before and carries on.
func TestTheExclusionIsCreatedOnce(t *testing.T) {
	root := t.TempDir()
	existing := &catalogv1alpha1.ImportExclusion{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat"},
		Spec: catalogv1alpha1.ImportExclusionSpec{Kind: catalogv1alpha1.ExclusionKindMovie,
			ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyTMDB: "949"}}}
	c := newClient(t, rootFolder(root), heatMovie(filepath.Join(root, "Heat"), catalogv1alpha1.DeleteRecords, true), existing)
	require.NoError(t, reconcileMovie(t, c))
	var list catalogv1alpha1.ImportExclusionList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Len(t, list.Items, 1)
}

// Only a new or renewed request passes -- never importarr's own
// delete-error write, which would loop a refusal forever.
func TestDeleteRequestedPredicate(t *testing.T) {
	p := deleteRequested()
	with := func(ann map[string]string) *catalogv1alpha1.Movie {
		return &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Annotations: ann}}
	}
	req := map[string]string{catalogv1alpha1.AnnotationDelete: "files"}
	refused := map[string]string{catalogv1alpha1.AnnotationDelete: "files", catalogv1alpha1.AnnotationDeleteError: "no"}
	assert.True(t, p.Create(event.CreateEvent{Object: with(req)}), "pending at start")
	assert.False(t, p.Create(event.CreateEvent{Object: with(refused)}), "refused before a restart")
	assert.False(t, p.Create(event.CreateEvent{Object: with(nil)}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: with(nil), ObjectNew: with(req)}), "a new request")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: with(req), ObjectNew: with(refused)}), "importarr's own write")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: with(refused), ObjectNew: with(req)}), "Retry clears the error")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: with(req), ObjectNew: with(req)}))
}

var _ = os.Remove // keep os imported for write()
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./app/import/controller/librarydelete/ -run 'Delete|Refus|Unknown|Exclusion|Predicate'`
Expected: build failure, `undefined: RegisterIndexes`, `undefined: Reconciler`, `undefined: kindFor`, `undefined: deleteRequested`.

- [ ] **Step 3: Implement `kinds.go`** (GPL header first)

```go
package librarydelete

import (
	"context"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The field indexes this controller reads, under names of its own: the
// Series and fileimport packages register similar ones, and a second
// registration under one name is an "indexer conflict" at start.
const (
	mediaFileByTarget = "librarydelete.spec.mediaRef.target"
	childByParent     = "librarydelete.parent"
)

// RegisterIndexes registers every index Reconcile reads, once, on idx.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, mediaFileByTarget, func(o client.Object) []string {
		mf, ok := o.(*catalogv1alpha1.MediaFile)
		if !ok || mf.Spec.MediaRef.Name == "" {
			return nil
		}
		ref := mf.Spec.MediaRef
		out := []string{TargetKey(ref.Kind, ref.Name)}
		for _, k := range ref.Keys {
			if k != ref.Name {
				out = append(out, TargetKey(ref.Kind, k))
			}
		}
		return out
	}); err != nil {
		return err
	}
	parents := []struct {
		obj    client.Object
		parent func(client.Object) string
	}{
		{&catalogv1alpha1.Episode{}, func(o client.Object) string { return o.(*catalogv1alpha1.Episode).Spec.SeriesRef }},
		{&catalogv1alpha1.Album{}, func(o client.Object) string { return o.(*catalogv1alpha1.Album).Spec.ArtistRef }},
		{&catalogv1alpha1.Book{}, func(o client.Object) string {
			if ref := o.(*catalogv1alpha1.Book).Spec.AuthorRef; ref != nil {
				return *ref
			}
			return ""
		}},
		{&catalogv1alpha1.Issue{}, func(o client.Object) string { return o.(*catalogv1alpha1.Issue).Spec.ComicRef }},
	}
	for _, p := range parents {
		if err := idx.IndexField(ctx, p.obj, childByParent, func(o client.Object) []string {
			if name := p.parent(o); name != "" {
				return []string{name}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// kindSpec is what the delete needs to know of one deletable kind.
type kindSpec struct {
	kind commonv1.MediaKind
	// newObject is an empty object of the kind.
	newObject func() client.Object
	// rootFolderRef and path read the item's RootFolder and status.path.
	rootFolderRef func(client.Object) string
	path          func(client.Object) string
	// childKind is the kind of its children and newChildren a list of them
	// ("" and nil for Movie and Audiobook).
	childKind   commonv1.MediaKind
	newChildren func() client.ObjectList
	// exclusion is the ImportExclusion spec that keeps it out; ok false for
	// a kind with none.
	exclusion func(client.Object) (catalogv1alpha1.ImportExclusionSpec, bool)
}

const exclusionReason = "deleted from the library"

func kindFor(kind commonv1.MediaKind) kindSpec {
	for _, k := range kinds() {
		if k.kind == kind {
			return k
		}
	}
	panic("librarydelete: no kind " + string(kind))
}

func kinds() []kindSpec {
	return []kindSpec{
		{
			kind:          commonv1.MediaKindMovie,
			newObject:     func() client.Object { return &catalogv1alpha1.Movie{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Movie).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Movie).Status.Path },
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				m := o.(*catalogv1alpha1.Movie)
				spec := catalogv1alpha1.ImportExclusionSpec{Kind: catalogv1alpha1.ExclusionKindMovie, Reason: exclusionReason,
					ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyTMDB: strconv.FormatInt(m.Spec.TmdbID, 10)}}
				if md := m.Status.Metadata; md != nil {
					spec.Title, spec.Year = md.Title, md.Year
				}
				return spec, true
			},
		},
		{
			kind:          commonv1.MediaKindSeries,
			newObject:     func() client.Object { return &catalogv1alpha1.Series{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Series).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Series).Status.Path },
			childKind:     commonv1.MediaKindEpisode,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.EpisodeList{} },
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				s := o.(*catalogv1alpha1.Series)
				spec := catalogv1alpha1.ImportExclusionSpec{Kind: catalogv1alpha1.ExclusionKindSeries, Reason: exclusionReason,
					ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyTVDB: strconv.FormatInt(s.Spec.TvdbID, 10)}}
				if md := s.Status.Metadata; md != nil {
					spec.Title, spec.Year = md.Title, md.Year
				}
				return spec, true
			},
		},
		{
			kind:          commonv1.MediaKindArtist,
			newObject:     func() client.Object { return &catalogv1alpha1.Artist{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Artist).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Artist).Status.Path },
			childKind:     commonv1.MediaKindAlbum,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.AlbumList{} },
			exclusion:     func(client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) { return catalogv1alpha1.ImportExclusionSpec{}, false },
		},
		{
			kind:          commonv1.MediaKindAuthor,
			newObject:     func() client.Object { return &catalogv1alpha1.Author{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Author).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Author).Status.Path },
			childKind:     commonv1.MediaKindBook,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.BookList{} },
			exclusion:     func(client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) { return catalogv1alpha1.ImportExclusionSpec{}, false },
		},
		{
			kind:          commonv1.MediaKindAudiobook,
			newObject:     func() client.Object { return &catalogv1alpha1.Audiobook{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Audiobook).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Audiobook).Status.Path },
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				ab := o.(*catalogv1alpha1.Audiobook)
				return catalogv1alpha1.ImportExclusionSpec{Kind: catalogv1alpha1.ExclusionKindAudiobook, Reason: exclusionReason,
					ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyASIN: ab.Spec.ASIN}}, true
			},
		},
		{
			kind:          commonv1.MediaKindComic,
			newObject:     func() client.Object { return &catalogv1alpha1.Comic{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Comic).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Comic).Status.Path },
			childKind:     commonv1.MediaKindIssue,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.IssueList{} },
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				cm := o.(*catalogv1alpha1.Comic)
				// Source is comicvine or mangadex, the same words as the
				// exclusion's id keys.
				return catalogv1alpha1.ImportExclusionSpec{Kind: catalogv1alpha1.ExclusionKindComic, Reason: exclusionReason,
					ExternalIDs: map[string]string{string(cm.Spec.Source): cm.Spec.SourceID}}, true
			},
		},
	}
}
```

- [ ] **Step 4: Implement `reconciler.go`** (GPL header first)

```go
package librarydelete

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;artists;authors;audiobooks;comics,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes;albums;books;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importexclusions,verbs=get;create
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler carries out the delete request on one kind.
type Reconciler struct {
	client.Client
	Recorder k8sevents.EventRecorder
	kind     kindSpec
}

// SetupWithManager registers the indexes once and a controller per
// deletable kind.
func SetupWithManager(mgr ctrl.Manager) error {
	if err := RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	for _, k := range kinds() {
		r := &Reconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("librarydelete"), kind: k}
		if err := ctrl.NewControllerManagedBy(mgr).
			Named("librarydelete-" + string(k.kind)).
			For(k.newObject(), builder.WithPredicates(deleteRequested())).
			Complete(r); err != nil {
			return fmt.Errorf("librarydelete %s: %w", k.kind, err)
		}
	}
	return nil
}

// deleteRequested passes a pending request: at start one not refused yet,
// and on update a new or renewed request -- never importarr's own
// delete-error write, which would retry a refusal forever.
func deleteRequested() predicate.Predicate {
	ann := func(o client.Object, key string) string { return o.GetAnnotations()[key] }
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return ann(e.Object, catalogv1alpha1.AnnotationDelete) != "" && ann(e.Object, catalogv1alpha1.AnnotationDeleteError) == ""
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, n := e.ObjectOld, e.ObjectNew
			if ann(n, catalogv1alpha1.AnnotationDelete) == "" {
				return false
			}
			return ann(n, catalogv1alpha1.AnnotationDelete) != ann(o, catalogv1alpha1.AnnotationDelete) ||
				(ann(o, catalogv1alpha1.AnnotationDeleteError) != "" && ann(n, catalogv1alpha1.AnnotationDeleteError) == "")
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// Reconcile deletes an annotated item: exclusion, disk (for "files"),
// MediaFiles, then the item. A refusal writes delete-error and is not
// retried; an API or I/O error writes it and is retried with backoff.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := r.kind.newObject()
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	mode := obj.GetAnnotations()[catalogv1alpha1.AnnotationDelete]
	if mode == "" || !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}
	log := logging.FromContext(ctx).With("kind", string(r.kind.kind), "item", obj.GetName(), "mode", mode)
	if mode != catalogv1alpha1.DeleteFiles && mode != catalogv1alpha1.DeleteRecords {
		return ctrl.Result{}, r.fail(ctx, obj, fmt.Errorf("%w: %s=%q, want %q or %q", ErrRefused,
			catalogv1alpha1.AnnotationDelete, mode, catalogv1alpha1.DeleteFiles, catalogv1alpha1.DeleteRecords))
	}

	t, err := r.target(ctx, obj)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, obj, err)
	}
	if mode == catalogv1alpha1.DeleteFiles {
		var all catalogv1alpha1.MediaFileList
		if err := r.List(ctx, &all, client.InNamespace(obj.GetNamespace())); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
		if err := Check(t, all.Items); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
	}
	if obj.GetAnnotations()[catalogv1alpha1.AnnotationDeleteAddExclusion] == "true" {
		if err := r.exclude(ctx, obj); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
	}
	if mode == catalogv1alpha1.DeleteFiles {
		if err := RemoveFromDisk(ctx, t); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
	}
	for i := range t.Files {
		if err := client.IgnoreNotFound(r.Delete(ctx, &t.Files[i])); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, fmt.Errorf("delete media file %s: %w", t.Files[i].Name, err))
		}
	}
	if err := client.IgnoreNotFound(r.Delete(ctx, obj)); err != nil {
		return ctrl.Result{}, r.fail(ctx, obj, err)
	}
	log.Info("librarydelete: deleted", "files", len(t.Files), "folder", t.Folder)
	r.event(obj, corev1.EventTypeNormal, "Deleted", "deleted with %d media file(s) (%s)", len(t.Files), mode)
	return ctrl.Result{}, nil
}

// target resolves what the delete acts on.
func (r *Reconciler) target(ctx context.Context, obj client.Object) (Target, error) {
	t := Target{Folder: r.kind.path(obj), Keys: map[string]bool{TargetKey(r.kind.kind, obj.GetName()): true}}
	var rf catalogv1alpha1.RootFolder
	switch err := r.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: r.kind.rootFolderRef(obj)}, &rf); {
	case err == nil:
		t.Root = rf.Spec.Path
	case !apierrors.IsNotFound(err):
		return t, err
	}
	if r.kind.newChildren != nil {
		children := r.kind.newChildren()
		if err := r.List(ctx, children, client.InNamespace(obj.GetNamespace()),
			client.MatchingFields{childByParent: obj.GetName()}); err != nil {
			return t, fmt.Errorf("list %s of %s: %w", r.kind.childKind, obj.GetName(), err)
		}
		items, err := metaList(children)
		if err != nil {
			return t, err
		}
		for _, name := range items {
			t.Keys[TargetKey(r.kind.childKind, name)] = true
		}
	}
	seen := map[string]bool{}
	for key := range t.Keys {
		var mfs catalogv1alpha1.MediaFileList
		if err := r.List(ctx, &mfs, client.InNamespace(obj.GetNamespace()), client.MatchingFields{mediaFileByTarget: key}); err != nil {
			return t, fmt.Errorf("list media files of %s: %w", key, err)
		}
		for _, mf := range mfs.Items {
			if !seen[mf.Name] {
				seen[mf.Name] = true
				t.Files = append(t.Files, mf)
			}
		}
	}
	return t, nil
}

// metaList is the names in a typed list.
func metaList(list client.ObjectList) ([]string, error) {
	raw, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Items []struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(decoded.Items))
	for _, it := range decoded.Items {
		out = append(out, it.Metadata.Name)
	}
	return out, nil
}

// exclude creates the item's ImportExclusion, named after it, once.
func (r *Reconciler) exclude(ctx context.Context, obj client.Object) error {
	spec, ok := r.kind.exclusion(obj)
	if !ok {
		return nil
	}
	ex := &catalogv1alpha1.ImportExclusion{
		ObjectMeta: metav1.ObjectMeta{Namespace: obj.GetNamespace(), Name: obj.GetName()},
		Spec:       spec,
	}
	if err := r.Create(ctx, ex); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create import exclusion: %w", err)
	}
	return nil
}

// fail records err on the item as delete-error plus a Warning Event. A
// refusal returns nil, so it waits for a renewed request; anything else is
// returned for the backoff retry.
func (r *Reconciler) fail(ctx context.Context, obj client.Object, err error) error {
	logging.FromContext(ctx).Warn("librarydelete: not deleted", "kind", string(r.kind.kind), "item", obj.GetName(), "error", err)
	r.event(obj, corev1.EventTypeWarning, "DeleteFailed", "%s", err.Error())
	patch, merr := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{catalogv1alpha1.AnnotationDeleteError: err.Error()},
	}})
	if merr == nil {
		target := r.kind.newObject()
		target.SetNamespace(obj.GetNamespace())
		target.SetName(obj.GetName())
		if perr := r.Patch(ctx, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(k8s.ManagerImportarr.String())); perr != nil {
			logging.FromContext(ctx).Error("librarydelete: record delete-error", "error", perr)
		}
	}
	if errors.Is(err, ErrRefused) {
		return nil
	}
	return err
}

func (r *Reconciler) event(obj client.Object, eventtype, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, nil, eventtype, reason, "Delete", format, args...)
	}
}
```

Check before committing: `k8s.ManagerImportarr` has a `String()` method (`pkg/k8s/fieldmanager.go`); if it is a plain string constant, use `string(k8s.ManagerImportarr)`.

- [ ] **Step 5: Wire it** in `app/import/run.go` `setupControllers`, after the importexclusion reconciler:

```go
	// The library delete (docs/superpowers/specs/2026-09-30-library-delete-
	// design.md): carries out catalog.clustarr.io/delete on a library item,
	// removing its folder on disk for "files", so it runs here, where the
	// library is mounted, under the lease.
	if err := librarydelete.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: librarydelete: %w", err)
	}
```

Add the import `"github.com/mediactl/clustarr/app/import/controller/librarydelete"`.

- [ ] **Step 6: Run the tests and regenerate RBAC**

Run: `go test ./app/import/... && make manifests && go test ./cmd/clustarr/ -run 'RBAC|Role|Chart'`
Expected: PASS. If a chart-versus-config RBAC test fails, copy the regenerated `config/rbac/importarr_role.yaml` rules into the chart's `charts/clustarr/templates/rbac.yaml` block for importarr and rerun.

- [ ] **Step 7: Commit**

```bash
git add app/import/controller/librarydelete/
git commit -m "feat(import): the librarydelete controller -- carries out catalog.clustarr.io/delete on a Movie, Series, Artist, Author, Audiobook or Comic: exclusion, folder removal for files, MediaFile records, then the item; a refusal or failure is written back as delete-error" -- app/import/controller/librarydelete/ app/import/run.go config/rbac/ charts/clustarr/templates/rbac.yaml
```

---

### Task 4: The library scan honours ImportExclusions

**Files:**
- Create: `app/import/worker/rescan/excluded.go`
- Create: `app/import/worker/rescan/excluded_internal_test.go`
- Modify: `app/import/worker/rescan/series.go` (`createSeries`, before the profile check)
- Modify: `app/import/worker/rescan/mediafile.go` (the `if created {` branch, before the profile check, ~line 253)

**Interfaces:**
- Consumes: `events.BucketImportExclusions`, `events.ExclusionKey(source, id string) string`, `events.DecodeExclusionEntry`.
- Produces: `const CodeExcluded = "excluded"`; `func (w *Worker) excluded(ctx context.Context, provider, id string) (events.ExclusionEntry, bool, error)`.

- [ ] **Step 1: Write the failing test** (`excluded_internal_test.go`, package `rescan`)

```go
package rescan

import (
	"context"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A folder whose TheTVDB id is excluded -- a series deleted with its files
// kept and "Add import list exclusion" ticked -- is recorded unmatched and
// no Series is created.
func TestCreateSeriesSkipsAnExcludedFolder(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	data, err := events.ExclusionEntry{Namespace: "tv", Name: "andor", Kind: "series"}.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketImportExclusions).Put(ctx, events.ExclusionKey(catalogv1alpha1.ExclusionIDKeyTVDB, "393189"), data)
	require.NoError(t, err)

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	w := &Worker{Client: c, Bus: bus}
	st := &scanState{
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "scan"}},
		root: &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "shows"},
			Spec: catalogv1alpha1.RootFolderSpec{Defaults: catalogv1alpha1.RootFolderDefaults{QualityProfileRef: "hd"}}},
	}
	got, err := w.createSeries(ctx, st, "Andor (2022) {tvdb-393189}/S01E01.mkv", &SeriesCandidate{TvdbID: 393189, Title: "Andor"})
	require.NoError(t, err)
	assert.Nil(t, got)
	var list catalogv1alpha1.SeriesList
	require.NoError(t, c.List(ctx, &list))
	assert.Empty(t, list.Items)
	require.Len(t, st.progress.Unmatched, 1)
	assert.Contains(t, st.progress.Unmatched[0].Reason, "excluded")
}
```

Check before running: the RootFolder defaults field name (`grep -n 'Defaults' api/catalog/v1alpha1/rootfolder_types.go`); use the real type name if it is not `RootFolderDefaults`.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./app/import/worker/rescan/ -run TestCreateSeriesSkipsAnExcludedFolder`
Expected: FAIL -- a Series was created (`list.Items` not empty) and nothing unmatched.

- [ ] **Step 3: Implement**

`excluded.go` (GPL header first):

```go
package rescan

import (
	"context"
	"errors"

	"github.com/mediactl/clustarr/pkg/events"
)

// CodeExcluded is a folder whose id an ImportExclusion names: the scan
// creates no item for it (a delete that kept the files and asked to stay
// out).
const CodeExcluded = "excluded"

// excluded looks provider/id up in the clustarr-import-exclusions bucket,
// the lookup the import-list sync makes. With no bus nothing is excluded.
func (w *Worker) excluded(ctx context.Context, provider, id string) (events.ExclusionEntry, bool, error) {
	if w.Bus == nil || id == "" {
		return events.ExclusionEntry{}, false, nil
	}
	entry, err := w.Bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey(provider, id))
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		return events.ExclusionEntry{}, false, nil
	case err != nil:
		return events.ExclusionEntry{}, false, err
	}
	decoded, err := events.DecodeExclusionEntry(entry.Value)
	return decoded, true, err
}
```

In `series.go` `createSeries`, first lines of the body:

```go
	if ex, ok, err := w.excluded(ctx, catalogv1alpha1.ExclusionIDKeyTVDB, strconv.FormatInt(want.TvdbID, 10)); err != nil {
		return nil, err
	} else if ok {
		st.unmatched(rel, CodeExcluded, fmt.Sprintf(
			"excluded: the series folder carries TheTVDB id %d, which import exclusion %s/%s keeps out",
			want.TvdbID, ex.Namespace, ex.Name), nil, w.now())
		return nil, nil
	}
```

In `mediafile.go`, first lines inside `if created {`:

```go
		if ex, ok, err := w.excluded(ctx, catalogv1alpha1.ExclusionIDKeyTMDB, strconv.FormatInt(result.TmdbID, 10)); err != nil {
			return err
		} else if ok {
			st.unmatched(rel, CodeExcluded, fmt.Sprintf(
				"excluded: the file names TMDB id %d, which import exclusion %s/%s keeps out",
				result.TmdbID, ex.Namespace, ex.Name), nil, now)
			return nil
		}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./app/import/worker/rescan/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/import/worker/rescan/excluded.go app/import/worker/rescan/excluded_internal_test.go
git commit -m "feat(import): the library scan honours ImportExclusions -- a folder whose TMDB or TheTVDB id is excluded is recorded unmatched 'excluded' instead of re-creating the item" -- app/import/worker/rescan/
```

---

### Task 5: The ui action and the Deleting state

**Files:**
- Create: `ui/actions/delete.go`
- Create: `ui/actions/delete_test.go`
- Modify: `ui/projection/library.go` (`LibraryItem` fields; `buildLibraryItems`)
- Modify: `ui/projection/library_view.go` (`LibraryStatus`)
- Modify: `ui/projection/library_view_test.go`

**Interfaces:**
- Consumes: Task 1 constants and `DeletableKinds`.
- Produces:
  - `func actions.RequestDelete(ctx, p Patcher, namespace string, kind commonv1.MediaKind, name string, files, exclude bool) (client.Object, error)` and `func (a *Actions) RequestDelete(ctx, namespace string, kind commonv1.MediaKind, name string, files, exclude bool) (client.Object, error)`.
  - `LibraryItem.DeleteMode string` (the annotation's value, "" when none), `LibraryItem.DeleteExclude bool`, `LibraryItem.DeleteError string`.
  - `LibraryStatus` returns `"deleting"` while `DeleteMode != ""`.

- [ ] **Step 1: Write the failing tests**

`ui/actions/delete_test.go`:

```go
package actions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

func TestRequestDeleteWritesTheAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "default",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDeleteError: "refused", "keep": "me"}},
		Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie).Build()
	ctx := context.Background()
	get := func() map[string]string {
		var m catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "heat"}, &m))
		return m.Annotations
	}

	_, err := actions.RequestDelete(ctx, c, "default", commonv1.MediaKindMovie, "heat", true, true)
	require.NoError(t, err)
	ann := get()
	require.Equal(t, catalogv1alpha1.DeleteFiles, ann[catalogv1alpha1.AnnotationDelete])
	require.Equal(t, "true", ann[catalogv1alpha1.AnnotationDeleteAddExclusion])
	require.NotContains(t, ann, catalogv1alpha1.AnnotationDeleteError, "a new request clears the old error")
	require.Equal(t, "me", ann["keep"])

	_, err = actions.RequestDelete(ctx, c, "default", commonv1.MediaKindMovie, "heat", false, false)
	require.NoError(t, err)
	ann = get()
	require.Equal(t, catalogv1alpha1.DeleteRecords, ann[catalogv1alpha1.AnnotationDelete])
	require.NotContains(t, ann, catalogv1alpha1.AnnotationDeleteAddExclusion)

	_, err = actions.RequestDelete(ctx, c, "default", commonv1.MediaKindEpisode, "x", true, false)
	require.True(t, errors.Is(err, actions.ErrInvalid), "an episode is deleted with its series: %v", err)
}
```

Append to `ui/projection/library_view_test.go`:

```go
func TestLibraryStatusReadsDeletingFirst(t *testing.T) {
	item := projection.LibraryItem{HasFile: true, Monitored: true, Phase: "Imported", DeleteMode: "files"}
	require.Equal(t, "deleting", projection.LibraryStatus(item))
	item.Kind = "series"
	require.Equal(t, "deleting", projection.LibraryStatus(item))
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./ui/actions/ ./ui/projection/ -run 'RequestDelete|Deleting'`
Expected: build failure, `undefined: actions.RequestDelete`, `unknown field DeleteMode`.

- [ ] **Step 3: Implement**

`ui/actions/delete.go` (GPL header first):

```go
package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// RequestDelete is the library Delete button (docs/superpowers/specs/
// 2026-09-30-library-delete-design.md): a merge patch of the item's
// annotations under [FieldManager] -- catalog.clustarr.io/delete ("files"
// with files, else "records"), delete-add-exclusion "true" with exclude
// (removed otherwise), and delete-error removed, so a Retry renews the
// request. importarr carries it out. It uses the item's existing patch
// grant; the ui never deletes.
func RequestDelete(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string, files, exclude bool,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.RequestDelete")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if !slices.Contains(catalogv1alpha1.DeletableKinds(), kind) {
		err := fmt.Errorf("%w: a %s is deleted with its parent", ErrInvalid, kind)
		tracing.RecordError(span, err)
		return nil, err
	}
	mode := catalogv1alpha1.DeleteRecords
	if files {
		mode = catalogv1alpha1.DeleteFiles
	}
	annotations := map[string]any{
		catalogv1alpha1.AnnotationDelete:             mode,
		catalogv1alpha1.AnnotationDeleteAddExclusion: nil,
		catalogv1alpha1.AnnotationDeleteError:        nil,
	}
	if exclude {
		annotations[catalogv1alpha1.AnnotationDeleteAddExclusion] = "true"
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
	if err != nil {
		err = fmt.Errorf("actions: encode delete request patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}
	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: request delete of %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: delete requested",
		"namespace", namespace, "kind", string(kind), "name", name, "files", files, "exclude", exclude)
	return obj, nil
}

// RequestDelete is [RequestDelete] over the Actions' own client.
func (a *Actions) RequestDelete(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string, files, exclude bool,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return RequestDelete(ctx, a.w, namespace, kind, name, files, exclude)
}
```

In `ui/projection/library.go`, add to `LibraryItem` after `DownloadingEpisodes`:

```go
	// DeleteMode is the item's catalog.clustarr.io/delete ("files" or
	// "records"), "" when no delete is pending; DeleteExclude its
	// delete-add-exclusion and DeleteError importarr's delete-error.
	DeleteMode    string
	DeleteExclude bool
	DeleteError   string
```

and in `buildLibraryItems`, before `out = append(...)`:

```go
		ann := item.GetAnnotations()
```

with the three fields in the literal:

```go
			DeleteMode:          ann[catalogv1.AnnotationDelete],
			DeleteExclude:       ann[catalogv1.AnnotationDeleteAddExclusion] == "true",
			DeleteError:         ann[catalogv1.AnnotationDeleteError],
```

In `ui/projection/library_view.go`, first lines of `LibraryStatus`:

```go
	if li.DeleteMode != "" {
		return "deleting"
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./ui/...`
Expected: PASS, `TestUINeverWrites` included (only `Patch` is called).

- [ ] **Step 5: Commit**

```bash
git add ui/actions/delete.go ui/actions/delete_test.go
git commit -m "feat(ui): the RequestDelete action and a library item's Deleting state -- a merge patch of the delete annotations; nothing is deleted by the ui" -- ui/actions/delete.go ui/actions/delete_test.go ui/projection/
```

---

### Task 6: The Delete button, dialog and route; docs

**Files:**
- Modify: `ui/views/detail.templ` (`itemToolbar`; `statusLabel`)
- Create: `ui/views/delete.templ`
- Modify: `ui/views/library.templ` (`stripeColour`: `"deleting"` -> `bg-slate-400`)
- Modify: `ui/routes.go` (route and handler)
- Create: `ui/delete_test.go`
- Modify: `CLAUDE.md` (the UI paragraph)
- Modify: `docs/superpowers/specs/2026-09-30-library-delete-design.md` (the dialog's file count: see Ruling below)

**Interfaces:**
- Consumes: `(*actions.Actions).RequestDelete`; `LibraryItem.DeleteMode/DeleteExclude/DeleteError`; `catalogv1.ExclusionKindFor`, `catalogv1.DeletableKinds`; `views.Detail{Item, Path, Files}`; `totalSize(files []FileRow) int64`.
- Produces: route `POST /library/{namespace}/{kind}/{name}/delete`, form fields `files` and `exclude` (`"true"` when ticked).

Ruling (spec deviation, record in the ledger): the dialog shows the file count and size only where the page already lists files (a movie's `Detail.Files`); for a series, artist, author, audiobook or comic it names the folder alone -- the spec's "from its MediaFiles" would add a MediaFile read to every page render for a number the folder already conveys. Amend the spec's ui bullet to say so in this task's commit.

- [ ] **Step 1: Write the failing test** (`ui/delete_test.go`, package `ui_test`; reuse `detailPage`, `section` and `requireTag` from `detail_test.go`)

```go
package ui_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

func deleteServer(t *testing.T, item projection.LibraryItem) *ui.Server {
	t.Helper()
	return ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	})
}

func TestEveryItemPageOffersDelete(t *testing.T) {
	for _, tc := range []struct {
		kind    commonv1.MediaKind
		tab     projection.Tab
		exclude bool
	}{
		{commonv1.MediaKindMovie, projection.TabMovies, true},
		{commonv1.MediaKindSeries, projection.TabTV, true},
		{commonv1.MediaKindArtist, projection.TabMusic, false},
		{commonv1.MediaKindAuthor, projection.TabBooks, false},
	} {
		item := projection.LibraryItem{Ref: types.NamespacedName{Namespace: "default", Name: "x"}, Kind: tc.kind, Tab: tc.tab, Title: "X", Monitored: true}
		body := detailPage(t, deleteServer(t, item), "/library/default/"+string(tc.kind)+"/x")
		requireTag(t, body, `data-action="delete"`)
		requireTag(t, body, `action="/library/default/`+string(tc.kind)+`/x/delete"`)
		requireTag(t, body, `name="files"`, `value="true"`)
		if tc.exclude {
			requireTag(t, body, `name="exclude"`, `value="true"`)
		} else {
			require.NotContains(t, body, `name="exclude"`, tc.kind)
		}
	}
}

func TestAPendingDeleteReadsDeletingAndAFailedOneOffersRetry(t *testing.T) {
	item := projection.LibraryItem{Ref: types.NamespacedName{Namespace: "default", Name: "heat"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Heat", DeleteMode: "files"}
	body := detailPage(t, deleteServer(t, item), "/library/default/movie/heat")
	require.Contains(t, section(t, body, `data-fact="status"`), "Deleting")

	item.DeleteError = "folder also holds media file ronin-file"
	item.DeleteExclude = true
	body = detailPage(t, deleteServer(t, item), "/library/default/movie/heat")
	require.Contains(t, section(t, body, `data-fact="status"`), "Delete failed: folder also holds media file ronin-file")
	requireTag(t, body, `data-action="delete-retry"`)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./ui/ -run 'OffersDelete|PendingDelete'`
Expected: FAIL, no `data-action="delete"` in the page.

- [ ] **Step 3: Implement the view** -- `ui/views/delete.templ`:

```templ
package views

import (
	"fmt"
	"slices"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/components/button"
	"github.com/mediactl/clustarr/ui/components/checkbox"
	"github.com/mediactl/clustarr/ui/components/dialog"
	"github.com/mediactl/clustarr/ui/components/icon"
)

// deletable is whether the item's kind has the Delete button.
func deletable(d Detail) bool { return slices.Contains(catalogv1.DeletableKinds(), d.Item.Kind) }

// excludable is whether the dialog offers "Add import list exclusion".
func excludable(d Detail) bool {
	_, ok := catalogv1.ExclusionKindFor(d.Item.Kind)
	return ok
}

// deleteDialog is the toolbar's Delete button and its confirmation
// (docs/superpowers/specs/2026-09-30-library-delete-design.md): the folder,
// the files where the page lists them, and the two checkboxes, both off.
templ deleteDialog(d Detail) {
	@dialog.Dialog(dialog.Props{ID: "delete-" + d.Item.Ref.Name}) {
		@button.Button(button.Props{Variant: button.VariantGhost, Class: toolbarButtonClass, Attributes: templ.Attributes{"data-action": "delete", "data-toolbar-button": true}.Merge(dialog.Trigger(ctx))}) {
			@icon.Icon("trash-2")()
			<span>Delete</span>
		}
		@dialog.Content() {
			@dialog.Header() {
				@dialog.Title() {
					{ "Delete " + d.Item.Title }
				}
				@dialog.Description() {
					if d.Path != "" {
						<span data-delete-folder>{ d.Path }</span>
					}
					if len(d.Files) > 0 {
						<span data-delete-files>{ fmt.Sprintf(" -- %d file(s), %s", len(d.Files), humanBytes(totalSize(d.Files))) }</span>
					}
				}
			}
			<form method="post" action={ templ.URL(libraryActionURL(d.Item, "delete")) } class="space-y-3">
				<input type="hidden" name="return" value={ "/library/" + string(d.Item.Tab) }/>
				<label class="flex items-start gap-2">
					@checkbox.Checkbox(checkbox.Props{ID: "delete-files-" + d.Item.Ref.Name, Name: "files", Value: "true"})
					<span>
						Delete files and folders
						<span class="block text-xs text-destructive">This permanently deletes the folder and cannot be undone.</span>
					</span>
				</label>
				if excludable(d) {
					<label class="flex items-center gap-2">
						@checkbox.Checkbox(checkbox.Props{ID: "delete-exclude-" + d.Item.Ref.Name, Name: "exclude", Value: "true"})
						<span>Add import list exclusion</span>
					</label>
				}
				@dialog.Footer() {
					@button.Button(button.Props{Variant: button.VariantOutline, Attributes: dialog.Close(ctx)}) { Cancel }
					@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantDestructive, Attributes: templ.Attributes{"data-action": "delete-confirm"}}) { Delete }
				}
			</form>
		}
	}
}

// deleteRetry re-sends a failed request as it was.
templ deleteRetry(d Detail) {
	<form method="post" action={ templ.URL(libraryActionURL(d.Item, "delete")) } class="inline">
		<input type="hidden" name="return" value={ libraryDetailPath(d.Item) }/>
		if d.Item.DeleteMode == catalogv1.DeleteFiles {
			<input type="hidden" name="files" value="true"/>
		}
		if d.Item.DeleteExclude {
			<input type="hidden" name="exclude" value="true"/>
		}
		@button.Button(button.Props{Type: button.TypeSubmit, Size: button.SizeXs, Variant: button.VariantOutline, Attributes: templ.Attributes{"data-action": "delete-retry"}}) { Retry }
	</form>
}
```

Check before writing: `humanBytes` is the byte formatter the files section uses (`grep -n 'func humanBytes\|func formatBytes\|func bytesLabel' ui/views/*.templ ui/views/*.go`) -- use whatever name it has; and `templ.Attributes` has no `Merge` in every templ version -- if it does not, build the map: `attrs := dialog.Trigger(ctx); attrs["data-action"] = "delete"; attrs["data-toolbar-button"] = true` inside a `{{ }}` block and pass `attrs`.

In `detail.templ` `itemToolbar`, after the last `@toolbarForm(...)` inside `@toolbarGroup()`:

```templ
			if deletable(d) && d.Item.DeleteMode == "" {
				@deleteDialog(d)
			}
```

In `statusLabel`, first case:

```go
	case "deleting":
		if li.DeleteError != "" {
			return "Delete failed: " + li.DeleteError
		}
		return "Deleting"
```

and in the hero's status fact (`detail.templ` line ~215, after `{ statusLabel(li) }`), render the retry when the delete failed:

```templ
							if li.DeleteError != "" {
								@deleteRetry(d)
							}
```

(The hero receives `d Detail`; if the status fact is rendered from `li` only, pass `d` down or render the retry beside the fact in `hero(d)`.)

In `library.templ` `stripeColour`, add `case "deleting": return "bg-slate-400"`.

- [ ] **Step 4: Implement the route** -- `ui/routes.go`, next to the other item actions:

```go
	mux.HandleFunc("POST /library/{namespace}/{kind}/{name}/delete", s.handleRequestDelete)
```

and the handler:

```go
// handleRequestDelete is the Delete dialog (docs/superpowers/specs/
// 2026-09-30-library-delete-design.md): POST
// /library/{namespace}/{kind}/{name}/delete with "files" and "exclude"
// ("true" when ticked), calling Options.Actions.RequestDelete; importarr
// carries it out. The outcome is finishAction's.
func (s *Server) handleRequestDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, err := s.opts.Actions.RequestDelete(r.Context(), r.PathValue("namespace"),
		commonv1.MediaKind(r.PathValue("kind")), r.PathValue("name"),
		r.FormValue("files") == "true", r.FormValue("exclude") == "true")
	s.finishAction(w, r, err)
}
```

- [ ] **Step 5: Generate and run**

Run: `make templ && go test ./ui/... && make lint`
Expected: PASS, 0 lint issues.

- [ ] **Step 6: Docs** -- in `CLAUDE.md`'s UI section, after the Add New paragraph:

```markdown
Each library item's page has **Delete**
(`docs/superpowers/specs/2026-09-30-library-delete-design.md`, 2026-09-30):
the ui only patches `catalog.clustarr.io/delete` (`files` or `records`)
and `delete-add-exclusion`; importarr's `librarydelete` controller removes
the folder permanently for `files` (never the RootFolder, refusing a path
outside it or a folder holding another item's MediaFile), deletes the
MediaFiles and then the item, or writes `delete-error`. The library scan
skips a folder whose id an ImportExclusion names (unmatched `excluded`).
```

Amend the spec's ui bullet ("its file count and size (from its MediaFiles)") to "its file count and size where the page lists its files (a movie's)".

- [ ] **Step 7: Commit**

```bash
git add ui/views/delete.templ ui/views/delete_templ.go ui/delete_test.go
git commit -m "feat(ui): the Delete button and dialog on every library item's page -- Delete files and folders and Add import list exclusion, both off; Deleting and Delete failed with Retry" -- ui/ CLAUDE.md docs/superpowers/specs/2026-09-30-library-delete-design.md
```
