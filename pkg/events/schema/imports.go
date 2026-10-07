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

package schema

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Import phases: the sub-key of an import record and the task it answers
// (ADR-0019 §6.9).
const (
	ImportSubInspect = "inspect"
	ImportSubExecute = "execute"
)

// ImportPlan modes (ImportPlan.Mode).
const (
	ImportModeHardlink = "hardlink"
	ImportModeMove     = "move"
	ImportModeCopy     = "copy"
)

// Refusals of an execute task (ImportExecution.Refused).
const (
	// ImportRefusedStale: a replaced MediaFile moved since the plan.
	ImportRefusedStale = "stale"
	// ImportRefusedDiskFull: the library volume has no room.
	ImportRefusedDiskFull = "diskFull"
	// ImportRefusedOverwrite: a destination exists and is not replaced.
	ImportRefusedOverwrite = "overwrite"
	// ImportRefusedOutsideRoot: a destination is outside the RootFolder.
	ImportRefusedOutsideRoot = "outsideRoot"
)

// ImportInspectTask asks the import agent to read a completed transfer's
// payload and report what each file is (ADR-0019 §6.9, step 1). Subject
// events.WorkImportInspectSubject, consumer importarr-fileimport, Msg-Id
// events.MsgIDForImport(entry uid, "inspect", seq). Pure computation: the
// answer goes to clustarr-imports at RecordSubKey(entry uid, "inspect").
type ImportInspectTask struct {
	Seq   int64    `json:"seq"`
	Owner ItemRef  `json:"owner"`
	Entry EntryRef `json:"entry"`
	// ContentRoot and Files are the transfer record's.
	ContentRoot string            `json:"contentRoot,omitempty"`
	OutputPath  string            `json:"outputPath,omitempty"`
	Files       []TransferFile    `json:"files,omitempty"`
	Release     CommandRelease    `json:"release"`
	Protocol    commonv1.Protocol `json:"protocol,omitempty"`
	// Targets are the covered Episodes or Issues, or the owner itself.
	Targets  []ItemRef   `json:"targets,omitempty"`
	Episodes []EpisodeNo `json:"episodes,omitempty"`
	Issues   []string    `json:"issues,omitempty"`
	// QualityProfile is the profile the grab was decided under.
	QualityProfile string                   `json:"qualityProfile,omitempty"`
	Purpose        commonv1.DownloadPurpose `json:"purpose,omitempty"`
	GrabbedBy      commonv1.GrabSource      `json:"grabbedBy,omitempty"`
	Manual         bool                     `json:"manual,omitempty"`
	Override       bool                     `json:"override,omitempty"`
	// TargetOverride is the download.clustarr.io/import intent's target.
	TargetOverride *ItemRef `json:"targetOverride,omitempty"`
}

// Schema implements Payload.
func (ImportInspectTask) Schema() string { return "importarr.ImportInspectTask.v1" }

// ImportExecuteTask carries the manager's approved plan to the import agent
// (ADR-0019 §6.9, step 3). Subject events.WorkImportExecuteSubject, consumer
// importarr-fileimport, Msg-Id events.MsgIDForImport(entry uid, "execute",
// seq); the answer goes to RecordSubKey(entry uid, "execute").
type ImportExecuteTask struct {
	Seq   int64      `json:"seq"`
	Owner ItemRef    `json:"owner"`
	Entry EntryRef   `json:"entry"`
	Plan  ImportPlan `json:"plan"`
}

// Schema implements Payload.
func (ImportExecuteTask) Schema() string { return "importarr.ImportExecuteTask.v1" }

// ImportPlan is what the import planner approved: the files to place, the
// MediaFiles they replace (the plan's basis: the agent re-reads each through
// the APIReader and refuses a stale plan), and an audio donor to reduce.
type ImportPlan struct {
	// Mode is ImportModeHardlink, ImportModeMove or ImportModeCopy.
	Mode     string           `json:"mode"`
	Moves    []ImportMove     `json:"moves,omitempty"`
	Replaces []MediaFileBasis `json:"replaces,omitempty"`
	Donor    *DonorPlan       `json:"donor,omitempty"`
	// RootFolder is the library root every destination must be under.
	RootFolder string `json:"rootFolder"`
	RecycleBin string `json:"recycleBin,omitempty"`
	// MinFreeBytes is the root folder's free-space floor (EnsureFreeSpace).
	MinFreeBytes int64 `json:"minFreeBytes,omitempty"`
}

// ImportMove places one file.
type ImportMove struct {
	Source string  `json:"source"`
	Dest   string  `json:"dest"`
	Target ItemRef `json:"target"`
	// Keys are the item names the file covers, more than one for a
	// multi-episode file (the MediaFile's spec.mediaRef.keys).
	Keys []string `json:"keys,omitempty"`
	// MediaFileName is the MediaFile the manager materialises for it.
	MediaFileName string       `json:"mediaFileName"`
	Frozen        FrozenFields `json:"frozen"`
}

// DonorPlan places an audio donor's payload for reduction: Source at Dest,
// under Dir (the item's donor folder).
type DonorPlan struct {
	Source string `json:"source"`
	Dir    string `json:"dir"`
	Dest   string `json:"dest,omitempty"`
}

// ProbeSummary is what an inspect reports of a file's probe: enough for the
// planner's TranscodedFinal, wrong-language and probe-corrected quality
// rules.
type ProbeSummary struct {
	VideoCodec      string   `json:"videoCodec,omitempty"`
	Width           int32    `json:"width,omitempty"`
	Height          int32    `json:"height,omitempty"`
	HDR             string   `json:"hdr,omitempty"`
	AudioLanguages  []string `json:"audioLanguages,omitempty"`
	DurationSeconds int64    `json:"durationSeconds,omitempty"`
	Transcoded      bool     `json:"transcoded,omitempty"`
	ProfileTag      string   `json:"profileTag,omitempty"`
}

// Kinds of an InspectRejection (InspectRejection.Kind).
const (
	// InspectKindBlocked: the item or its root folder cannot take the files
	// (a target that does not exist, a path that cannot be rendered); the
	// whole import needs a person.
	InspectKindBlocked = "blocked"
	// InspectKindSample: a suspected sample by size, incidental once real
	// media is beside it.
	InspectKindSample = "sample"
	// InspectKindIncidental: explains a file and decides nothing.
	InspectKindIncidental = "incidental"
)

// InspectRejection is one reason a file could not be imported, with its
// remediation class (commonv1.ImportRejectionClass) and error kind.
type InspectRejection struct {
	Class string `json:"class"`
	Text  string `json:"text"`
	Kind  string `json:"kind,omitempty"`
}

// InspectedFile is one file of a payload, as the import agent read it.
type InspectedFile struct {
	Path        string        `json:"path"`
	SizeBytes   int64         `json:"sizeBytes,omitempty"`
	Parsed      ParsedFacts   `json:"parsed"`
	Probe       *ProbeSummary `json:"probe,omitempty"`
	Fingerprint string        `json:"fingerprint,omitempty"`
	// Proposed is the target the file matched: the MediaFile's
	// spec.mediaRef kind and name, Keys the item names the file covers
	// (a multi-episode file's episodes; else the one name).
	Proposed *ItemRef `json:"proposed,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	// Dest is the library path the agent rendered for the file
	// (pkg/naming, from the full probe); the planner checks it, never
	// renders it.
	Dest       string             `json:"dest,omitempty"`
	Frozen     FrozenFields       `json:"frozen"`
	Sample     bool               `json:"sample,omitempty"`
	Rejections []InspectRejection `json:"rejections,omitempty"`
}

// ImportInspection is an inspect task's answer: every file, and the
// rejections of the import as a whole (a target that does not exist, a
// content root that cannot be read).
type ImportInspection struct {
	Files      []InspectedFile    `json:"files,omitempty"`
	Rejections []InspectRejection `json:"rejections,omitempty"`
	// RootFolder and RecycleBin are the target's root folder path and
	// recycle bin, as the agent resolved them.
	RootFolder string `json:"rootFolder,omitempty"`
	RecycleBin string `json:"recycleBin,omitempty"`
	// MinFreeBytes is the root folder's free-space floor.
	MinFreeBytes int64 `json:"minFreeBytes,omitempty"`
}

// PlacedFile is one file an execute task placed.
type PlacedFile struct {
	Source        string       `json:"source"`
	Dest          string       `json:"dest"`
	SizeBytes     int64        `json:"sizeBytes,omitempty"`
	ModTime       time.Time    `json:"modTime,omitzero"`
	Fingerprint   string       `json:"fingerprint,omitempty"`
	ProbeHash     string       `json:"probeHash,omitempty"`
	Target        ItemRef      `json:"target"`
	Keys          []string     `json:"keys,omitempty"`
	MediaFileName string       `json:"mediaFileName"`
	Frozen        FrozenFields `json:"frozen"`
}

// ImportExecution is an execute task's answer: what it placed, or why it
// refused (ImportRefused*), the files it recycled and a reduced donor's
// path.
type ImportExecution struct {
	Placed    []PlacedFile `json:"placed,omitempty"`
	Refused   string       `json:"refused,omitempty"`
	Recycled  []string     `json:"recycled,omitempty"`
	DonorPath string       `json:"donorPath,omitempty"`
	// Replaced are the plan's bases the execute checked and recycled: the
	// manager deletes each MediaFile with its UID precondition.
	Replaced []MediaFileBasis `json:"replaced,omitempty"`
}

// ImportRecord is clustarr-imports' value for one entry's import phase,
// keyed events.RecordSubKey(entry uid, ImportSubInspect|ImportSubExecute),
// written only by the import agent (ADR-0019 §6.9): Item is the owner, Sub
// the phase, Seq the task's.
type ImportRecord struct {
	RecordHeader
	Entry   EntryRef          `json:"entry"`
	Inspect *ImportInspection `json:"inspect,omitempty"`
	Execute *ImportExecution  `json:"execute,omitempty"`
}

// Schema implements Payload and is RecordHeader.Schema on every value.
func (ImportRecord) Schema() string { return "importarr.Import.v1" }
