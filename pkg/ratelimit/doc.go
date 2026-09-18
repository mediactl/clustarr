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

// Package ratelimit is Clustarr's rate-limiting toolkit for talking to
// indexers and download-client APIs: a keyed token-bucket limiter over
// golang.org/x/time/rate, an exponential Backoff with jitter and a
// server Retry-After override, and a closed/open/half-open
// CircuitBreaker for a dependency that is failing outright rather than
// merely rate-limiting.
//
// None of the three types talk to each other; a caller composes them
// (limiter for steady-state pacing, breaker for outright failures,
// backoff for the delay between retries) the way indexarr's health/limit
// bookkeeping does per docs/research/indexers.md §6.
package ratelimit
