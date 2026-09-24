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

// Package pool is the pure renderer for squasharr's pool Jobs: one long-lived
// work-queue Job per (TranscodeProfile, hardware class), whose pods run
// cmd/squasharr-worker and pull tasks from NATS (spec
// docs/superpowers/specs/2026-09-23-transcode-worker-design.md, §3 and §7).
//
// It renders the pod template ([Template]), classifies drift between what a
// profile now asks for and what a stored Job was last applied with
// ([Classify]), builds the one complete server-side-apply declaration for a
// pool Job ([Render]), and decides one admission pass's desired shape and
// action ([Next]). Nothing here talks to the apiserver: it is pure functions
// over typed values, so it is tested without envtest.
package pool
