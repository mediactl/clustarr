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

// Package quality resolves Clustarr's TRaSH-shaped QualityProfile CRDs into
// ready-to-evaluate Profile values: a video/music/book/audiobook/comic
// quality ladder (Definition, Lookup), TRaSH per-minute size tables
// (SizeLimit, SizeLimits), a QualityProfile's tiers and custom-format scores
// resolved against pkg/quality/catalogue (FromCRD; only a video profile has
// custom formats -- the catalogue is TRaSH's Radarr/Sonarr data), and the
// single-candidate
// upgrade decision ported from UpgradableSpecification.IsUpgradable
// (Profile.UpgradeDecision). The 13 built-in profiles this task ships as
// embedded data are decoded and resolved by BuiltinProfiles.
//
// This package is pure Go: it imports pkg/release (to read
// release.ParsedRelease.Quality/Revision/Languages/Group/ReleaseType) and,
// for the profile spec shape only, api/catalog/v1alpha1 -- never a
// Kubernetes client. Custom-format matching itself (regexp2 compilation,
// *arr group semantics, the embedded TRaSH corpus subset) lives in the
// sibling package pkg/quality/catalogue.
package quality
