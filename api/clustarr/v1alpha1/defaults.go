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
	ReasonProviderNotFound = "ProviderNotFound"

	ReasonDisabled          = "Disabled"
	ReasonSecretNotFound    = "SecretNotFound"
	ReasonSecretKeyNotFound = "SecretKeyNotFound"
	ReasonInvalidServer     = "InvalidServer"
	ReasonInvalidGuideURL   = "InvalidGuideURL"

	// A provider's Ready=False reasons from its blocks (Live TV spec §3.1).
	// Each keeps the last good status.blocks, so no Plex DVR is removed.
	ReasonTooManyChannels    = "TooManyChannels"
	ReasonDuplicateDeviceID  = "DuplicateDeviceID"
	ReasonNumberRangeOverlap = "NumberRangeOverlap"
)

// AnnotationLiveTVRefresh on an IPTVProvider forces a fetch of every
// source when its value changes: the ui's "Refresh now" writes the
// requester's Unix time (the direct-tuners and ui design §5.2). Declared
// here because the writer (ui/actions) and the reader (the provider
// controller) both name it.
const AnnotationLiveTVRefresh = "clustarr.io/livetv-refresh"

// The keys of a provider's credentials Secret.
const (
	CredentialsKeyUsername = "username"
	CredentialsKeyPassword = "password"
)

// The defaults the accessors apply to an unset field. A field the CRD could
// default is a pointer or an omitempty zero read through these, because a
// typed Go client cannot send a value the CRD default would override.
const (
	DefaultChannelNumberStart int32 = 1000
	DefaultRefresh                  = 24 * time.Hour
	DefaultUserAgent                = "clustarr"
	DefaultStreamBuffer       int64 = 8 << 20
	DefaultStreamLinger             = 10 * time.Second
	DefaultDummyMinutes       int32 = 60
	DefaultMaxChannels        int32 = 400
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

// MaxChannelsOrDefault is spec.dvr.maxChannels, 400 when unset.
func (s IPTVProviderSpec) MaxChannelsOrDefault() int32 {
	if s.DVR.MaxChannels == nil {
		return DefaultMaxChannels
	}
	return *s.DVR.MaxChannels
}

// ChannelNumberStartOrDefault is spec.channelNumberStart, 1000 when unset.
func (s IPTVProviderSpec) ChannelNumberStartOrDefault() int32 {
	if s.ChannelNumberStart == nil {
		return DefaultChannelNumberStart
	}
	return *s.ChannelNumberStart
}

// RefreshOrDefault is spec.refresh, 24h when unset or not positive.
func (s IPTVProviderSpec) RefreshOrDefault() time.Duration {
	if s.Refresh == nil || s.Refresh.Duration <= 0 {
		return DefaultRefresh
	}
	return s.Refresh.Duration
}

// UserAgentOrDefault is spec.userAgent, "clustarr" when unset.
func (s IPTVProviderSpec) UserAgentOrDefault() string {
	if s.UserAgent == "" {
		return DefaultUserAgent
	}
	return s.UserAgent
}

// TunersOverride is spec.tuners, 0 when unset: the account's own limit.
func (s IPTVProviderSpec) TunersOverride() int {
	if s.Tuners == nil {
		return 0
	}
	return int(*s.Tuners)
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

// DefaultStreamLanguage is the audio language declared in a stream whose
// program map names none or an undetermined one.
const DefaultStreamLanguage = "eng"

// LanguageOrDefault is the language the relay fills into a stream, "eng"
// when unset.
func (s StreamSpec) LanguageOrDefault() string {
	if s.Language == "" {
		return DefaultStreamLanguage
	}
	return s.Language
}

// DummyMinutesOrDefault is the dummy guide's block length, 60 when unset.
func (e ChannelEPG) DummyMinutesOrDefault() int32 {
	if e.DummyMinutes == nil {
		return DefaultDummyMinutes
	}
	return *e.DummyMinutes
}
