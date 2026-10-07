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

package searchoutcome

import (
	"fmt"
	"unicode/utf8"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// MaxIndexerOutcomes mirrors status.indexerOutcomes' own
// +kubebuilder:validation:MaxItems=100 and caps SearchRecord.IndexerOutcomes
// (schema.MaxIndexerOutcomes).
const MaxIndexerOutcomes = schema.MaxIndexerOutcomes

// maxOutcomeErrorBytes bounds one outcome's error message. status is not a
// log sink, and a hundred multi-kilobyte indexer errors would make the
// object itself a problem.
const maxOutcomeErrorBytes = 512

// Name is the name an outcome is reported under: the Indexer object's
// name, or failing that its display name ("" when the index named it
// neither). The search worker's own entries (WorkerOutcomeName,
// TruncatedOutcomeName) ride in IndexerName.
func Name(o schema.SearchOutcome) string {
	if o.IndexerRef.Name != "" {
		return o.IndexerRef.Name
	}
	return o.IndexerName
}

// Map projects a search record's per-indexer report onto the API type
// (ADR-0019 §7.4: the Search controller incorporates it). An outcome with
// no indexer name is kept under a reserved name (UnnamedOutcomeName)
// numbered in reply order: status.indexerOutcomes is listType=map keyed by
// name, and the reserved names contain a slash, so none collides with a
// real Indexer's.
func Map(outcomes []schema.SearchOutcome) []catalogv1alpha1.IndexerOutcome {
	out := make([]catalogv1alpha1.IndexerOutcome, 0, len(outcomes))
	unnamed := 0
	for _, o := range outcomes {
		name := Name(o)
		if name == "" {
			unnamed++
			name = UnnamedOutcomeName(unnamed)
		}
		out = append(out, catalogv1alpha1.IndexerOutcome{
			Name:       name,
			State:      state(o.Status),
			Count:      o.Releases,
			DurationMs: int32(o.ElapsedMillis), //nolint:gosec // a search's milliseconds fit
			Error:      TruncateError(o.Error),
		})
	}
	return Cap(out)
}

// Truncation is the outcome a search worker appends when the index cut
// the federated reply at schema.MaxSearchReleases: the results were decided
// from a partial set. It takes the last slot of a full list, since a
// truncation nobody can see is the failure the marker exists to prevent.
func Truncation(outcomes []schema.SearchOutcome, releases int) []schema.SearchOutcome {
	marker := schema.SearchOutcome{
		IndexerName: TruncatedOutcomeName,
		Status:      schema.SearchOutcomeSkipped,
		Releases:    int32(releases), //nolint:gosec // capped at MaxSearchReleases
		Error: fmt.Sprintf("index cut the federated reply at %d releases; any release beyond that was never decided",
			schema.MaxSearchReleases),
	}
	if len(outcomes) >= MaxIndexerOutcomes {
		outcomes = outcomes[:MaxIndexerOutcomes-1]
	}
	return append(outcomes, marker)
}

// Cap deduplicates by name (first wins) and truncates to
// MaxIndexerOutcomes: a repeated name or a 101st entry makes the apiserver
// reject the whole status apply.
func Cap(outcomes []catalogv1alpha1.IndexerOutcome) []catalogv1alpha1.IndexerOutcome {
	out := make([]catalogv1alpha1.IndexerOutcome, 0, min(len(outcomes), MaxIndexerOutcomes))
	seen := make(map[string]struct{}, len(outcomes))
	for _, o := range outcomes {
		if o.Name == "" {
			continue
		}
		if _, dup := seen[o.Name]; dup {
			continue
		}
		seen[o.Name] = struct{}{}
		out = append(out, o)
		if len(out) == MaxIndexerOutcomes {
			break
		}
	}
	return out
}

// TruncateError bounds one error message to maxOutcomeErrorBytes, cutting
// on a rune boundary.
func TruncateError(msg string) string {
	if len(msg) <= maxOutcomeErrorBytes {
		return msg
	}
	cut := maxOutcomeErrorBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + "..."
}

func state(s schema.SearchOutcomeStatus) catalogv1alpha1.IndexerOutcomeState {
	switch s {
	case schema.SearchOutcomeOK:
		return catalogv1alpha1.IndexerOutcomeOK
	case schema.SearchOutcomeTimeout:
		return catalogv1alpha1.IndexerOutcomeTimeout
	case schema.SearchOutcomeError:
		return catalogv1alpha1.IndexerOutcomeError
	default:
		return catalogv1alpha1.IndexerOutcomeSkipped
	}
}
