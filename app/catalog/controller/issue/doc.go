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

// Package issue implements the Issue controller: acquisition state as a pure
// function, and a thin Reconciler around it plus the file/download rollup
// from a watched MediaFile and Download -- mirroring
// app/catalog/controller/episode exactly for the Comic -> Issue pair (task
// G2-2).
//
// Issue objects are created and owned by the Comic controller (Comic sets
// spec.comicRef/number/calculatedNumberCentis and, at creation or per
// spec.monitorNewIssues, spec.monitored), and the Comic reconciler also
// writes this Issue's provider-sourced status fields
// (sourceID/title/date) -- but under the distinct
// k8s.ManagerCatalogarrFanout field manager, never k8s.ManagerCatalogarr,
// which this reconciler alone uses for
// State/Conditions/HasFile/FileRef/FileQuality/ActiveDownloadRef. Server-side
// apply replaces a manager's whole ownership set on every apply, so two
// writers sharing one manager name would silently release each other's
// fields the next time either side reconciles -- the same empirical finding
// documented on k8s.ManagerCatalogarrSeries and CLAUDE.md, reused here via
// k8s.ManagerCatalogarrFanout rather than rediscovered. This reconciler
// never builds an IssueStatusApplyConfiguration that calls
// WithSourceID/WithTitle/WithDate -- those are the Comic reconciler's alone.
//
// Both reconcilers write within the SAME subresource (status): every field
// the Comic reconciler sets on an Issue is genuinely an IssueStatus field,
// confirmed against issue_types.go -- IssueSpec carries only
// comicRef/number/calculatedNumberCentis (immutable) and monitored, both set
// once at Create rather than through repeated server-side apply, so they
// carry none of the same-manager clobbering risk the status fields do. This
// is the same status-versus-status split as episode's doc comment describes
// for Series -> Episode, not MediaFile's spec-versus-status one.
//
// Like Episode, an Issue is ranked against a profile it does not name: its
// Comic's spec.qualityProfileRef. This reconciler resolves that profile
// (resolveProfile) and writes the outcome to status.cutoffMet and the
// CutoffMet condition, so a comic file below its profile's cutoff stays an
// upgrade candidate. IssueStatus has no FileFormatScore leaf, and a comic
// profile scores no custom formats (pkg/quality.FromCRD gives every
// non-video profile an empty Scores), so that return value of
// rollup.FileState is not read.
package issue
