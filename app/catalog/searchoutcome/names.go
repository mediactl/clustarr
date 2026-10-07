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

// Package searchoutcome holds the status.indexerOutcomes names the search
// worker reserves, which the Search controller reads back. Both sides
// import it, so the worker links no controller (design 2026-10-06 §4.3 C9).
package searchoutcome

import (
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/legacynames"
)

// WorkerOutcomeName is the status.indexerOutcomes entry name the search worker
// reserves for a failure that is not any one indexer's fault -- an invalid
// QualityProfile, an unsupported media kind, a target that vanished mid-flight.
//
// The value is deliberately not a valid DNS-1123 subdomain, so it can never
// collide with a real Indexer object's name in a listType=map keyed by name.
// Chart 0.4.x wrote it under the catalog service's old name; readers go
// through IsWorkerOutcome, which accepts both through release N.
const WorkerOutcomeName = "catalog/search-worker"

// TruncatedOutcomeName is the status.indexerOutcomes entry the search worker
// adds when the index agent cut the federated reply at
// schema.MaxSearchReleases: the results were decided from a partial set, and
// that has to be visible on the object rather than only in a log line.
// Reserved the same way as WorkerOutcomeName; read through
// IsTruncatedOutcome.
const TruncatedOutcomeName = "catalog/truncated"

// reservedOutcomePrefix begins every outcome name the search worker reserves
// for something that is not one named Indexer. The slash keeps each of them
// out of the DNS-1123 namespace a real Indexer's name lives in.
const reservedOutcomePrefix = "catalog/"

// UnnamedOutcomeName is the status.indexerOutcomes name the search worker
// gives the n-th (1-based, in reply order) outcome the index agent reported
// with no indexer name at all -- an indexer that failed before it could be
// named. Such an outcome used to be dropped, which hid exactly the failure
// an operator most needs to see.
func UnnamedOutcomeName(n int) string {
	return reservedOutcomePrefix + "unnamed-indexer-" + strconv.Itoa(n)
}

// IsWorkerOutcome reports whether name is WorkerOutcomeName, or the name
// chart 0.4.x wrote for it on a Search that outlived the upgrade
// (unbranded-names spec §4.6; N+1 drops the legacy half).
func IsWorkerOutcome(name string) bool {
	return name == WorkerOutcomeName || legacynames.SearchOutcomeNames[name] == WorkerOutcomeName
}

// IsTruncatedOutcome reports whether name is TruncatedOutcomeName, or its
// chart 0.4.x spelling.
func IsTruncatedOutcome(name string) bool {
	return name == TruncatedOutcomeName || legacynames.SearchOutcomeNames[name] == TruncatedOutcomeName
}

// Reserved reports whether name is one the search worker reserves rather
// than a real Indexer's: it begins with the reserved prefix, or with chart
// 0.4.x's (legacynames.SearchOutcomePrefix).
func Reserved(name string) bool {
	return strings.HasPrefix(name, reservedOutcomePrefix) || strings.HasPrefix(name, legacynames.SearchOutcomePrefix)
}
