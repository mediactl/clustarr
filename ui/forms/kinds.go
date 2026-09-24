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

package forms

import "github.com/mediactl/clustarr/ui/actions"

// The overlays: one per Settings kind, in the page's order. Paths are the
// CRD's spec paths; TestEveryConfigKindHasAnOverlayWhosePathsExist holds
// each to the schema, so a typo fails a test rather than dropping a field.

func configKind(slug string) actions.ConfigKind {
	k, ok := actions.ConfigKindBySlug(slug)
	if !ok {
		panic("forms: no config kind " + slug)
	}
	return k
}

var (
	whenTorrent = &When{Path: "protocol", Values: []string{"torrent"}}
	whenUsenet  = &When{Path: "protocol", Values: []string{"usenet"}}
)

var kinds = []Kind{
	{
		ConfigKind: configKind("rootfolders"),
		Title:      "Root folders", Singular: "root folder",
		Help: "A directory under /data/media that holds one kind of media. It must already exist and be writable; the path and kind cannot change once created.",
		Groups: []Group{
			{Title: "Folder", Paths: []string{"path", "kind", "scanSchedule", "minFreeBytes"}},
			{Title: "Defaults for new items", Paths: []string{
				"defaults.qualityProfileRef", "defaults.transcodeProfileRef", "defaults.subtitleProfileRef", "defaults.delayProfileRef",
				"defaults.monitored", "defaults.monitorNewItems", "defaults.searchOnAdd", "defaults.minimumAvailability",
				"defaults.seriesType", "defaults.seasonFolder", "defaults.tags",
			}},
			{Title: "Naming", Paths: []string{"naming.dialect", "naming.colonReplacement", "naming.multiEpisodeStyle", "naming.overrides"}, Advanced: true},
			{Title: "Recycle bin", Paths: []string{"recycleBin.path", "recycleBin.cleanupDays"}, Advanced: true},
			{Title: "Permissions", Paths: []string{"permissions.fileMode", "permissions.dirMode", "permissions.group"}, Advanced: true},
		},
		Labels: map[string]string{"scanSchedule": "Scan schedule (cron)", "minFreeBytes": "Minimum free bytes", "naming.overrides": "Naming token overrides"},
		Refs: map[string]Ref{
			"defaults.qualityProfileRef": RefQualityProfiles, "defaults.transcodeProfileRef": RefTranscodeProfiles,
			"defaults.subtitleProfileRef": RefSubtitleProfiles, "defaults.delayProfileRef": RefDelayProfiles,
		},
		ReadOnlyOnEdit: []string{"path", "kind"},
	},
	{
		ConfigKind: configKind("qualityprofiles"),
		Title:      "Quality profiles", Singular: "quality profile",
		Help: "Tiers of qualities, best first, and the cutoff tier that stops upgrades. Built-in TRaSH profiles cannot be edited; copy one to change it. Custom-format scoring is TRaSH's and not editable here.",
		Groups: []Group{
			{Title: "Profile", Paths: []string{"mediaKind", "cutoff", "upgradeAllowed", "language", "preferredProtocol", "properPolicy", "sizeTable", "scoreSet"}},
			{Title: "Tiers", Help: "Best first. Each tier lists the quality names it accepts, one per line.", Paths: []string{"tiers"}},
			{Title: "Custom formats", Paths: []string{"minFormatScore", "cutoffFormatScore", "minUpgradeFormatScore", "enabledFormatGroups"}, Advanced: true},
			{Title: "Size limits", Paths: []string{"sizeLimits"}, Advanced: true},
		},
		Hidden:         []string{"builtIn", "formatScores"},
		Labels:         map[string]string{"cutoff": "Cutoff tier", "scoreSet": "Score set", "sizeTable": "Size table"},
		ReadOnlyOnEdit: []string{"mediaKind"},
	},
	{
		ConfigKind: configKind("metadataproviders"),
		Title:      "Metadata providers", Singular: "metadata provider",
		Help: "A source of titles, artwork and ratings. The type cannot change once created. The metadata gateway reads providers at start, so a new or changed provider is used after catalogarr-metadata restarts.",
		Groups: []Group{
			{Title: "Provider", Paths: []string{"type", "enabled", "priority", "baseURL", "language", "region", "contactUserAgent"}},
			{Title: "Credentials", Paths: []string{"secretRef"}},
			{Title: "Rate limit", Paths: []string{"rateLimit.requestsPerSecond", "rateLimit.burst", "rateLimit.perDay"}, Advanced: true},
			{Title: "Cache", Paths: []string{"cacheTTL"}, Advanced: true},
		},
		Labels: map[string]string{"contactUserAgent": "Contact user agent (musicbrainz, openlibrary)", "rateLimit.perDay": "Requests per day"},
		Secrets: []Secret{{Path: "secretRef", Keys: []Key{
			{Key: "apiKey", Label: "API key", When: &When{Path: "type", Values: []string{"tmdb", "tvdb", "comicvine", "fanart", "mdblist", "omdb"}}},
			{Key: "pin", Label: "PIN", Help: "TVDB subscriber PIN, if any.", When: &When{Path: "type", Values: []string{"tvdb"}}},
			{Key: "bearer", Label: "Bearer token", When: &When{Path: "type", Values: []string{"hardcover", "metron"}}},
		}}},
		ReadOnlyOnEdit: []string{"type"},
	},
	{
		ConfigKind: configKind("indexers"),
		Title:      "Indexers", Singular: "indexer",
		Help: "A tracker or Newznab/Torznab server. Pick a bundled Cardigann definition, or leave it blank for a generic Newznab/Torznab upstream.",
		Groups: []Group{
			{Title: "Indexer", Paths: []string{"definition", "baseURL", "enabled", "priority", "downloadClientRef", "proxyRef", "tags"}},
			{Title: "Generic Newznab/Torznab", Help: "For an upstream with no definition.", Paths: []string{"generic.protocol", "generic.apiPath"}, When: &When{Path: "definition", Values: []string{""}}},
			{Title: "Credentials", Help: "Whatever the definition needs; a generic upstream needs its API key.", Paths: []string{"secretRef"}},
			{Title: "Search", Paths: []string{"enableRss", "enableAutomaticSearch", "enableInteractiveSearch", "rssInterval", "categories", "animeCategories", "animeStandardFormatSearch", "minimumSeeders"}},
			{Title: "Limits", Paths: []string{"limits.queryLimit", "limits.grabLimit", "limits.unit", "requestDelay", "timeout"}, Advanced: true},
			{Title: "Seeding", Paths: []string{"seedCriteria"}, Advanced: true},
			{Title: "Definition settings", Help: "Non-secret settings of the chosen definition, by name.", Paths: []string{"settings", "definitionRef"}, Advanced: true},
		},
		Labels: map[string]string{
			"definition": "Cardigann definition", "definitionRef": "IndexerDefinition object", "enableRss": "Enable RSS",
			"enableAutomaticSearch": "Enable automatic search", "enableInteractiveSearch": "Enable interactive search",
			"rssInterval": "RSS interval", "categories": "Categories (Newznab ids)", "animeCategories": "Anime categories (Newznab ids)",
			"settings": "Definition settings", "limits.unit": "Limit window",
		},
		Refs: map[string]Ref{"definition": RefIndexerDefinitions, "downloadClientRef": RefDownloadClients, "proxyRef": RefIndexerProxies},
		Secrets: []Secret{{Path: "secretRef", Keys: []Key{
			{Key: "apikey", Label: "API key"},
			{Key: "username", Label: "Username"},
			{Key: "password", Label: "Password"},
			{Key: "passkey", Label: "Passkey"},
			{Key: "cookie", Label: "Cookie", Help: "A logged-in session cookie, for a definition whose login needs a captcha."},
			{Key: "rss_key", Label: "RSS key"},
		}}},
		Ensure: func(spec map[string]any) {
			if d, _ := spec["definition"].(string); d != "" {
				delete(spec, "generic")
				delete(spec, "definitionRef")
			}
		},
	},
	{
		ConfigKind: configKind("downloadclients"),
		Title:      "Download clients", Singular: "download client",
		Help: "An embedded torrent or usenet engine grabarr runs for you. The protocol cannot change once created.",
		Groups: []Group{
			{Title: "Client", Paths: []string{"protocol", "enabled", "priority", "categories"}},
			{Title: "Torrent", When: whenTorrent, Paths: []string{
				"torrent.listenPort", "torrent.publicIP", "torrent.maxActive", "torrent.downloadLimitBps", "torrent.uploadLimitBps",
				"torrent.enableDHT", "torrent.enablePEX", "torrent.removeCompleted", "torrent.stallTimeout", "torrent.maxUnverifiedBytes", "torrent.seed",
			}},
			{Title: "Usenet", When: whenUsenet, Paths: []string{
				"usenet.providers", "usenet.postProcess", "usenet.propagationDelay", "usenet.preCheck", "usenet.abortHealthPercent",
				"usenet.healthAction", "usenet.downloadTimeout", "usenet.scratch",
			}},
			{Title: "Engine", Paths: []string{"replicas"}, Advanced: true},
		},
		Hidden: []string{"resources", "nodeSelector", "tolerations"},
		Labels: map[string]string{
			"categories": "Category folders (kind → subdirectory)", "torrent.listenPort": "Listen port", "torrent.publicIP": "Public IP",
			"torrent.downloadLimitBps": "Download limit (bytes/s)", "torrent.uploadLimitBps": "Upload limit (bytes/s)",
			"usenet.providers": "Servers", "usenet.providers[].quotaBytes": "Monthly quota (bytes)", "usenet.providers[].backup": "Backup server",
			"usenet.abortHealthPercent": "Abort below health (%)", "usenet.scratch.sizeLimit": "Scratch size",
		},
		Secrets: []Secret{{Path: "usenet.providers[].secretRef", Keys: []Key{
			{Key: "username", Label: "Username", Required: true},
			{Key: "password", Label: "Password", Required: true},
		}}},
		ReadOnlyOnEdit: []string{"protocol"},
		Ensure: func(spec map[string]any) {
			switch spec["protocol"] {
			case "torrent":
				delete(spec, "usenet")
				if _, ok := spec["torrent"]; !ok {
					spec["torrent"] = map[string]any{}
				}
			case "usenet":
				delete(spec, "torrent")
				if _, ok := spec["usenet"]; !ok {
					spec["usenet"] = map[string]any{}
				}
			}
		},
	},
	{
		ConfigKind: configKind("subtitleproviders"),
		Title:      "Subtitle providers", Singular: "subtitle provider",
		Help: "A subtitle source captionarr searches. The type cannot change once created.",
		Groups: []Group{
			{Title: "Provider", Paths: []string{"type", "enabled", "priority", "endpoint", "languages", "requestsPerSecondMilli"}},
			{Title: "Credentials", Paths: []string{"secretRef"}},
			{Title: "Options", Help: "aiTranslated (exclude or include), trustedSources (true).", Paths: []string{"options"}, Advanced: true},
		},
		Labels: map[string]string{"requestsPerSecondMilli": "Requests per second (×1000)", "languages": "Languages (BCP-47, one per line; blank = all)"},
		Secrets: []Secret{{Path: "secretRef", Keys: []Key{
			{Key: "apiKey", Label: "API key", When: &When{Path: "type", Values: []string{"opensubtitlescom", "subdl", "subsource"}}},
			{Key: "username", Label: "Username", When: &When{Path: "type", Values: []string{"opensubtitlescom"}}},
			{Key: "password", Label: "Password", When: &When{Path: "type", Values: []string{"opensubtitlescom"}}},
		}}},
		ReadOnlyOnEdit: []string{"type"},
	},
	{
		ConfigKind: configKind("subtitleprofiles"),
		Title:      "Subtitle profiles", Singular: "subtitle profile",
		Help: "Which languages to want and how hard to look. Only one profile may be the default.",
		Groups: []Group{
			{Title: "Profile", Paths: []string{"default", "cutoff", "hiExtension", "originalFormat", "providers", "selector.matchLabels"}},
			{Title: "Languages", Paths: []string{"languages", "languageEquals"}},
			{Title: "Matching", Paths: []string{"minScorePercent", "mustContain", "mustNotContain", "mods"}, Advanced: true},
			{Title: "Embedded tracks", Paths: []string{"embedded"}, Advanced: true},
			{Title: "Search and upgrades", Paths: []string{"search", "upgrade"}, Advanced: true},
		},
		Hidden: []string{"sync", "whisper", "selector.matchExpressions"},
		Labels: map[string]string{"default": "Cluster default", "cutoff": "Cutoff language key", "hiExtension": "Hearing-impaired infix", "providers": "Providers (in order, one per line)", "selector.matchLabels": "Applies to files labelled"},
	},
	{
		ConfigKind: configKind("transcodeprofiles"),
		Title:      "Transcode profiles", Singular: "transcode profile",
		Help: "How squasharr re-encodes the files its selector matches. Changing an encoding field re-queues every matching file.",
		Groups: []Group{
			{Title: "Profile", Paths: []string{"default", "priority", "maxConcurrent", "hardware", "container", "selector.matchLabels"}},
			{Title: "Video", Paths: []string{"video.codec", "video.pixelFormat", "video.profile", "video.preset", "video.tune", "video.crf", "video.maxRateKbps", "video.bufSizeKbps"}},
			{Title: "Video (x265 tuning)", Paths: []string{"video.keyintFactor", "video.bFrames", "video.refs", "video.rcLookahead", "video.aqMode", "video.extraX265Params"}, Advanced: true},
			{Title: "NVENC", Paths: []string{"video.nvenc"}, Advanced: true},
			{Title: "Intel QSV", Paths: []string{"video.qsv"}, Advanced: true},
			{Title: "Audio", Paths: []string{"audio"}},
			{Title: "Subtitles", Paths: []string{"subtitles"}, Advanced: true},
			{Title: "HDR", Paths: []string{"hdr"}, Advanced: true},
			{Title: "Policy", Paths: []string{"policy"}, Advanced: true},
			{Title: "Verify", Paths: []string{"verify"}, Advanced: true},
			{Title: "Resources", Paths: []string{"resources.limits", "resources.requests", "scratch", "activeDeadline", "gpu.count", "gpu.runtimeClassName"}, Advanced: true},
		},
		Hidden: []string{"chunking", "ttlSecondsAfterFinished", "gpu.nodeSelector", "gpu.tolerations", "resources.claims", "selector.matchExpressions"},
		Labels: map[string]string{"default": "Cluster default", "maxConcurrent": "Max concurrent jobs", "selector.matchLabels": "Applies to files labelled", "video.crf.hdrOffset": "CRF offset for HDR", "video.maxRateKbps": "Max rate (kbps)", "video.bufSizeKbps": "Buffer size (kbps)"},
	},
}
