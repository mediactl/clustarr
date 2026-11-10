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

// Package nfo is the remediation loop's nfo stage
// (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md §8.2):
// for an Artist, Album, Author or Audiobook with a folder on disk it
// renders the Kodi-shape artist.nfo or album.nfo (pkg/nfo) and the images
// beside it, asks the import agent to write them (an NFOWriteTask on
// import-nfo through the dispatch ledger, the request in clustarr-nfo), and
// incorporates the answer into status.nfo under catalog-nfo. A document
// whose hash status.nfo already records is written once and never again;
// a RootFolder with naming.writeNFO false plans nothing and removes
// nothing.
package nfo

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/nfo"
	"github.com/mediactl/clustarr/pkg/records"
)

// Rendered is what the stage rendered for an item: the documents and the
// images that go beside them, and their hash.
type Rendered struct {
	Files  []schema.NFOFile
	Images []schema.NFOImage
	Hash   string
}

// Input is everything Plan decides from.
type Input struct {
	Item schema.ItemRef
	// Folder is the item's folder on /data (status.path); "" plans nothing.
	Folder string
	// Enabled is the RootFolder's naming.writeNFO for a music or audiobook
	// root; false plans nothing and removes nothing.
	Enabled bool
	// Mode is the root's file mode for the written files, 0 for the default.
	Mode uint32
	// Rendered is the current rendering; nil when the item has no metadata
	// yet, which plans nothing.
	Rendered *Rendered
	// Prev is status.nfo as last written, nil when never.
	Prev *catalogv1alpha1.NFOStatus
	// Record is the clustarr-nfo record and its revision, when one exists.
	Record    *schema.NFORecord
	RecordRev uint64
	RecordOK  bool
	// Now is the pass's clock.
	Now time.Time
}

// Decision is what Plan decided.
type Decision struct {
	// Status is the status.nfo to write (incorporating an answer); nil
	// leaves it alone.
	Status *catalogv1alpha1.NFOStatus
	// Request is the task to publish, with its record to write first.
	Request *schema.NFOWriteTask
	// Answered reports a dispatch to release in the ledger: the record
	// answered the outstanding request.
	Answered bool
	// Due is when to look again; zero is no timer.
	Due time.Time
}

// RequestTimeout is how long an outstanding write is waited for before it
// is asked again under a new seq.
const RequestTimeout = 15 * time.Minute

// Plan decides one item's pass:
//
//   - nothing to do without a folder, metadata or the root's leave;
//   - the record's answer at the outstanding seq is incorporated into
//     status.nfo (hash, path, files written) and the dispatch released;
//   - a rendering whose hash status.nfo already records, at the same
//     folder, is done;
//   - otherwise a new request: unless one is outstanding and younger than
//     RequestTimeout, in which case wait.
//
// A failed answer is retried after RequestTimeout from the failure: the
// agent's own redeliveries cover the transient ones first.
func Plan(in Input) Decision {
	var d Decision
	if !in.Enabled || in.Folder == "" || in.Rendered == nil {
		return d
	}
	if in.RecordOK && in.Record != nil && in.Record.Seq > 0 {
		rec := in.Record
		// A terminal record releases its dispatch every pass until the
		// ledger has let it go: the release is idempotent, and a pass whose
		// apply landed but whose effects did not would otherwise leak the
		// slot until the audit.
		d.Answered = records.TerminalAnswered(rec.State)
		switch rec.State {
		case records.StateAnswered:
			if rec.Answer != nil && (in.Prev == nil || in.Prev.Hash != rec.Request.Hash || in.Prev.Path != rec.Request.Folder) {
				d.Status = &catalogv1alpha1.NFOStatus{
					Hash:  rec.Request.Hash,
					Path:  rec.Request.Folder,
					Files: mergedFiles(in.Prev, rec.Request.Folder, rec.Answer.Files),
				}
				if !rec.Answer.WrittenAt.IsZero() {
					t := metav1.NewTime(rec.Answer.WrittenAt.UTC())
					d.Status.WrittenAt = &t
				}
				if rec.Request.Hash == in.Rendered.Hash && rec.Request.Folder == in.Folder {
					return d
				}
			}
		case records.StateFailed:
			if rec.AnsweredAt != nil && in.Now.Sub(*rec.AnsweredAt) < RequestTimeout {
				d.Due = rec.AnsweredAt.Add(RequestTimeout)
				return d
			}
		case records.StateRequested, records.StateClaimed:
			if rec.Request.Hash == in.Rendered.Hash && rec.Request.Folder == in.Folder && in.Now.Sub(rec.RequestedAt) < RequestTimeout {
				d.Due = rec.RequestedAt.Add(RequestTimeout)
				return d
			}
		}
	}
	prev := in.Prev
	if d.Status != nil {
		prev = d.Status
	}
	if prev != nil && prev.Hash == in.Rendered.Hash && prev.Path == in.Folder {
		return d
	}
	var owned []string
	if prev != nil && prev.Path == in.Folder {
		owned = prev.Files
	}
	seq := in.Now.Unix()
	if in.RecordOK && in.Record != nil && in.Record.Seq >= seq {
		seq = in.Record.Seq + 1
	}
	d.Request = &schema.NFOWriteTask{
		Item: in.Item, Folder: in.Folder, Hash: in.Rendered.Hash, Mode: in.Mode, Seq: seq,
		Files: in.Rendered.Files, Images: in.Rendered.Images, Owned: owned,
	}
	return d
}

// mergedFiles is the names the agent has written at folder: the answer's
// plus what status.nfo held for the same folder (a kept file was written
// by an earlier answer and stays ours).
func mergedFiles(prev *catalogv1alpha1.NFOStatus, folder string, written []string) []string {
	out := append([]string(nil), written...)
	if prev == nil || prev.Path != folder {
		return out
	}
	for _, f := range prev.Files {
		found := false
		for _, w := range out {
			if w == f {
				found = true
				break
			}
		}
		if !found {
			out = append(out, f)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// Record is the clustarr-nfo request for t.
func Record(t *schema.NFOWriteTask) *schema.NFORecord {
	rec := &schema.NFORecord{Request: schema.NFOWriteRequest{Folder: t.Folder, Hash: t.Hash}}
	for _, f := range t.Files {
		rec.Request.Files = append(rec.Request.Files, f.Name)
	}
	for _, i := range t.Images {
		rec.Request.Images = append(rec.Request.Images, i.Name)
	}
	h := rec.Header()
	h.Schema, h.Item, h.Seq, h.State = schema.NFORecord{}.Schema(), &t.Item, t.Seq, records.StateRequested
	return rec
}

// ImageFor is the image to copy beside a document: name is the file name
// in the folder, object the artwork store object, digest its digest.
func ImageFor(name, object, digest string) schema.NFOImage {
	return schema.NFOImage{Name: name, Object: object, Digest: digest}
}

// HashOf is nfo.Hash over the files and images of a rendering.
func HashOf(files []schema.NFOFile, images []schema.NFOImage) string {
	docs := make([][]byte, 0, len(files))
	for _, f := range files {
		docs = append(docs, f.Content)
	}
	digests := make([]string, 0, len(images))
	for _, i := range images {
		digests = append(digests, i.Name+"="+i.Digest)
	}
	return nfo.Hash(docs, digests)
}
