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

// Package release parses release titles — scene, P2P and streaming-service
// names for movies, TV (standard, daily and anime), music, books,
// audiobooks and comics — into a structured ParsedRelease. It ports the
// *arr SourceRegex/ResolutionRegex/RemuxRegex/ReleaseGroupRegex/Proper-
// Repack-Real-Version families (GPL, see docs/research/quality.md §7) with
// github.com/dlclark/regexp2, and uses github.com/moistari/rls only as a
// tokenizer/hint extractor for custom-format tags (codec, HDR, audio) and
// for music/book/comic type detection — never for the quality identity
// itself, and never for anime groups, audiobooks or PROPER/REPACK/REAL
// revisions, all three of which docs/research/quality.md §7.3 verified rls
// gets wrong.
package release
