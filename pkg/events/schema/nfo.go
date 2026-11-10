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

import "time"

// The NFO write (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md
// §8): the remediation loop's nfo stage renders an Artist's or Album's
// artist.nfo or album.nfo (an Author's and an Audiobook's under an
// audiobook root) with pkg/nfo, publishes an NFOWriteTask on
// events.WorkNFOSubject (consumer events.ConsumerImportNFO) through the
// dispatch ledger, and the import agent writes every file it was handed
// with fsops.AtomicWrite, fetches each image from the artwork object store
// read-only, and answers in events.BucketNFO. The manager renders every
// byte; the agent decides nothing.

// NFOWriteTask is one item's write.
type NFOWriteTask struct {
	// Item is the Artist, Album, Author or Audiobook, UID included: the
	// record is keyed by it.
	Item ItemRef `json:"item"`
	// Folder is the item's folder on /data, where every file is written.
	Folder string `json:"folder"`
	// Files are the documents to write, rendered by the manager.
	Files []NFOFile `json:"files"`
	// Images are the artwork objects to copy beside them.
	Images []NFOImage `json:"images,omitempty"`
	// Hash is pkg/nfo's hash of the documents and the images' digests; the
	// answer echoes it and the loop records it in status.nfo.hash.
	Hash string `json:"hash"`
	// Mode is the file mode every written file carries (the RootFolder's
	// perms.fileMode); 0 reads as 0664.
	Mode uint32 `json:"mode,omitempty"`
	// Seq is the requested record's sequence; an answer for an older one is
	// dropped.
	Seq int64 `json:"seq"`
}

// Schema implements Payload.
func (NFOWriteTask) Schema() string { return "import.NFOWriteTask.v1" }

// NFOFile is one document to write: its name in the folder and its bytes
// (base64 on the wire).
type NFOFile struct {
	Name    string `json:"name"`
	Content []byte `json:"content"`
}

// NFOImage is one artwork object to copy beside the documents.
type NFOImage struct {
	// Name is the file name in the folder (folder.jpg, artist.jpg).
	Name string `json:"name"`
	// Bucket and Object name the artwork store object.
	Bucket string `json:"bucket"`
	Object string `json:"object"`
	// Digest is the object's digest when the task was built; the agent
	// writes the object it reads whatever its digest, and the hash above
	// moves when the digest does.
	Digest string `json:"digest,omitempty"`
}

// NFORecord is one item's record in clustarr-nfo, keyed
// events.RecordKey(item UID): the loop's request and the agent's answer.
type NFORecord struct {
	RecordHeader
	Request NFOWriteRequest `json:"request"`
	Answer  *NFOWriteAnswer `json:"answer,omitempty"`
}

// Schema implements Payload and is RecordHeader.Schema on every value.
func (NFORecord) Schema() string { return "import.NFORecord.v1" }

// NFOWriteRequest is what the loop asked for.
type NFOWriteRequest struct {
	Folder string   `json:"folder"`
	Hash   string   `json:"hash"`
	Files  []string `json:"files"`
	Images []string `json:"images,omitempty"`
}

// NFOWriteAnswer is what the agent wrote.
type NFOWriteAnswer struct {
	// Files are the names written, the only ones a later write rewrites.
	Files []string `json:"files"`
	// Kept are the names the task asked for that already existed and were
	// not the agent's own from an earlier write, so they were left alone
	// (an operator's folder.jpg; design §8.2).
	Kept      []string  `json:"kept,omitempty"`
	WrittenAt time.Time `json:"writtenAt"`
}
