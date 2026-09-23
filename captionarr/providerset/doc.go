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

// Package providerset turns SubtitleProvider custom resources, and the
// Secrets they reference, into pkg/subtitles.Provider clients.
//
// It is a separate package from captionarr/worker/fetch so that the
// SubtitleProvider controller can validate a provider ([Validate]) with
// exactly the checks the fetch worker's [Builder.Entry] runs before building
// a client -- one switch over SubtitleProviderType, not two that drift.
//
// # What it builds
//
// [Builder.Build] lists the enabled SubtitleProviders in a namespace and
// returns one [Entry] per provider that has a client, ordered by
// spec.priority ascending, ties broken by name. Three provider types have no
// client in this phase (ruling R5): subdl, subsource and whisper. They are
// skipped with [ErrNoClient], not failed -- one unimplemented provider must
// not take the working ones down with it.
//
// A remote provider (opensubtitlescom, gestdown) gets one long-lived client
// per SubtitleProvider object, cached across fetch tasks and rebuilt only
// when the object's generation or its Secret's resourceVersion changes. The
// cache is not an optimisation: the OpenSubtitles client holds its login
// token in memory, and OpenSubtitles rate-limits logins far harder than
// searches, so a client built per task would log in once per subtitle.
//
// The local provider (embedded) reads the media file itself, so it cannot
// be built until the file is known. Its [Entry] carries a constructor
// instead of a client; [Entry.Provider] resolves either shape.
//
// # Rate limiting is not this package's job
//
// Every client is built with a nil limiter (ruling R3: the provider
// packages default none). The caller paces requests through the shared
// clustarr-provider-throttle token bucket (captionarr/throttle.Acquire),
// keyed by [Entry.UID], so N worker replicas share one budget per
// SubtitleProvider rather than each holding a private one.
//
// # Secrets
//
// Secrets are read through [Builder.SecretReader], which the wiring task
// should point at the manager's API reader (mgr.GetAPIReader()), not its
// cached client: a cached Get of a Secret starts a cluster-wide Secret
// informer, which needs list/watch on every Secret and holds all of them in
// the worker's memory. The API reader needs only `get`.
//
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
package providerset
