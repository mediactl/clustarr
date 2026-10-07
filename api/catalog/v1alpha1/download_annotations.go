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

package v1alpha1

// User intent on a grab, set on the owning item (ADR-0019 §6.10). The loop
// never writes these annotations; the item sources wake on a change to any
// of them. Entry ids are DNS subdomains, so commas and spaces separate them.
// One-shot nonces follow ValidNonce and are recorded through HandledNonce in
// status.downloadNonces. Parsing is app/grab/lifecycle's, not this package's.
const (
	// AnnotationDownloadPaused is standing: "<id>[,<id>…]", the entries
	// whose transfers are paused (the command's paused). It replaces
	// Download spec.paused.
	AnnotationDownloadPaused = "download.clustarr.io/paused"
	// AnnotationDownloadPriority is standing:
	// "<id>=high|normal|low[,…]" (the command's priority). It replaces
	// Download spec.priority.
	AnnotationDownloadPriority = "download.clustarr.io/priority"
	// AnnotationDownloadRemove is one-shot: "<nonce> <id> [data]
	// [blocklist]" (downloadNonces.remove). It replaces deleting a Download
	// and labelling it blocklisted.
	AnnotationDownloadRemove = "download.clustarr.io/remove"
	// AnnotationDownloadResume is one-shot: "<nonce> <id>"
	// (downloadNonces.resume), releasing a health hold.
	AnnotationDownloadResume = "download.clustarr.io/resume"
	// AnnotationDownloadImport is one-shot: "<nonce> <id>
	// [target=<kind>/<name>[/<key>]] [override]" (downloadNonces.import).
	// It replaces catalog.clustarr.io/import-target and import-override.
	AnnotationDownloadImport = "download.clustarr.io/import"
	// AnnotationDownloadUnblock is one-shot: "<nonce> <infoHash>" or
	// "<nonce> <indexer>/<guid>", "[global]" (downloadNonces.unblock,
	// recorded only after the release index confirms, §6.14).
	AnnotationDownloadUnblock = "download.clustarr.io/unblock"
)

// FinalizerTransfers holds an owning item while it has grab entries, so its
// transfers are torn down before it goes (ADR-0019 §6.8). Written under
// field manager clustarr (k8s.DefaultFieldOwner, k8s.EnsureFinalizer).
const FinalizerTransfers = "download.clustarr.io/transfers"

// FieldDownloadPhase is the selectable field and print column every item
// kind carries: the active entry's phase, "" when none (ADR-0019 §6.2).
const FieldDownloadPhase = "status.downloadPhase"

// DownloadIntentAnnotations returns the six download intent annotation
// keys in a fixed order, for the loop's item sources, which wake on them at
// PriorityUser.
func DownloadIntentAnnotations() []string {
	return []string{
		AnnotationDownloadPaused, AnnotationDownloadPriority, AnnotationDownloadRemove,
		AnnotationDownloadResume, AnnotationDownloadImport, AnnotationDownloadUnblock,
	}
}
