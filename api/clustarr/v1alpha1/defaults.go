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

import "time"

// Condition types on IPTVProvider and IPTVChannel.
const (
	ConditionReady           = "Ready"
	ConditionValid           = "Valid"
	ConditionPlaylistFetched = "PlaylistFetched"
	ConditionGuidesFetched   = "GuidesFetched"
)

// Reasons of an IPTVChannel's Ready condition, and of a provider's.
const (
	ReasonActive           = "Active"
	ReasonInactive         = "Inactive"
	ReasonMissing          = "Missing"
	ReasonDuplicateNumber  = "DuplicateNumber"
	ReasonGuideNotFound    = "GuideNotFound"
	ReasonProviderNotFound = "ProviderNotFound"

	ReasonDisabled          = "Disabled"
	ReasonSecretNotFound    = "SecretNotFound"
	ReasonSecretKeyNotFound = "SecretKeyNotFound"
)

// The defaults the accessors apply to an unset field. A field the CRD could
// default is a pointer or an omitempty zero read through these, because a
// typed Go client cannot send a value the CRD default would override.
const (
	DefaultChannelNumberStart int32 = 1000
	DefaultPlaylistRefresh          = 24 * time.Hour
	DefaultGuideRefresh             = 12 * time.Hour
	DefaultUserAgent                = "clustarr"
	DefaultStreamBuffer       int64 = 8 << 20
	DefaultStreamLinger             = 10 * time.Second
	DefaultDummyMinutes       int32 = 60
)

// EnabledOrDefault is spec.enabled, true when unset.
func (s IPTVProviderSpec) EnabledOrDefault() bool { return s.Enabled == nil || *s.Enabled }

// EPGSourceOrDefault is spec.epgSource, XEPG when unset.
func (s IPTVProviderSpec) EPGSourceOrDefault() EPGSource {
	if s.EPGSource == "" {
		return EPGSourceXEPG
	}
	return s.EPGSource
}

// ChannelNumberStartOrDefault is spec.channelNumberStart, 1000 when unset.
func (s IPTVProviderSpec) ChannelNumberStartOrDefault() int32 {
	if s.ChannelNumberStart == nil {
		return DefaultChannelNumberStart
	}
	return *s.ChannelNumberStart
}

// RefreshOrDefault is the playlist's refresh, 24h when unset.
func (p PlaylistSource) RefreshOrDefault() time.Duration {
	if p.Refresh == nil || p.Refresh.Duration <= 0 {
		return DefaultPlaylistRefresh
	}
	return p.Refresh.Duration
}

// UserAgentOrDefault is the user agent, "clustarr" when unset.
func (p PlaylistSource) UserAgentOrDefault() string {
	if p.UserAgent == "" {
		return DefaultUserAgent
	}
	return p.UserAgent
}

// RefreshOrDefault is the guide's refresh, 12h when unset.
func (x XMLTVSource) RefreshOrDefault() time.Duration {
	if x.Refresh == nil || x.Refresh.Duration <= 0 {
		return DefaultGuideRefresh
	}
	return x.Refresh.Duration
}

// EnabledOrDefault is the filter's enabled, true when unset.
func (f ChannelFilter) EnabledOrDefault() bool { return f.Enabled == nil || *f.Enabled }

// FriendlyNameOrDefault is the device's friendly name, "Clustarr <name>"
// when unset.
func (d DeviceSpec) FriendlyNameOrDefault(name string) string {
	if d.FriendlyName == "" {
		return "Clustarr " + name
	}
	return d.FriendlyName
}

// BufferOrDefault is each channel's ring buffer in bytes, 8 MiB when unset
// or not positive.
func (s StreamSpec) BufferOrDefault() int64 {
	if s.Buffer == nil || s.Buffer.Value() <= 0 {
		return DefaultStreamBuffer
	}
	return s.Buffer.Value()
}

// LingerOrDefault is how long an unwatched upstream stays open, 10s when
// unset.
func (s StreamSpec) LingerOrDefault() time.Duration {
	if s.Linger == nil || s.Linger.Duration < 0 {
		return DefaultStreamLinger
	}
	return s.Linger.Duration
}

// DummyMinutesOrDefault is the dummy guide's block length, 60 when unset.
func (e ChannelEPG) DummyMinutesOrDefault() int32 {
	if e.DummyMinutes == nil {
		return DefaultDummyMinutes
	}
	return *e.DummyMinutes
}
