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

import (
	"slices"

	"github.com/mediactl/clustarr/ui/actions"
)

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

// MetaProtocol is the hidden input an Indexer form's definition section
// carries: the chosen definition's protocol, "" with none chosen.
const MetaProtocol = "__meta.protocol"

// when holds while the field at path has one of values.
func when(path string, values ...string) *When { return &When{Path: path, Values: values} }

// unless holds while the field at path has none of values.
func unless(path string, values ...string) *When {
	return &When{Path: path, Values: values, Not: true}
}

// indexerNot holds while the indexer is not known to be of protocol:
// neither its generic upstream nor its chosen definition says so. A generic
// section hidden behind a definition reads as unset.
func indexerNot(protocol string) *When {
	return and(unless("generic.protocol", protocol), unless(MetaProtocol, protocol))
}

// videoKinds are the root folder kinds a video quality profile serves.
var videoKinds = []string{"movie", "series"}

// profileForRoot offers a quality profile of media kind class to a root
// folder of the kinds it serves.
func profileForRoot(class string) *When {
	switch class {
	case "":
		return nil
	case "video":
		return when("kind", videoKinds...)
	}
	return when("kind", class)
}

// profileForList offers a quality profile of media kind class to an import
// list that adds a kind it serves.
func profileForList(class string) *When {
	switch class {
	case "":
		return nil
	case "video":
		return when("kinds", "movie", "series")
	case "music":
		return when("kinds", "album")
	}
	return when("kinds", class)
}

// rootForList offers a root folder of kind class to an import list that
// adds what it holds.
func rootForList(class string) *When {
	switch class {
	case "":
		return nil
	case "music":
		return when("kinds", "album")
	}
	return when("kinds", class)
}

var (
	whenTorrent = &When{Path: "protocol", Values: []string{"torrent"}}
	whenUsenet  = &When{Path: "protocol", Values: []string{"usenet"}}
	// importListProviders are ImportListSpec's provider sub-objects, exactly
	// one of which the CRD requires; the form's Choice offers them in this
	// order, and only these credentials are read: import's provider.go
	// takes clientID and clientSecret for Trakt (the device flow writes the
	// tokens itself), token and an optional clientID for Plex, apiKey for
	// MDBList; TMDB, StevenLu, IMDb CSV, custom and *arr lists need none.
	importListProviders = []string{"trakt", "plex", "tmdb", "mdblist", "stevenLu", "imdbCSV", "custom", "arr"}
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
				"defaults.seriesType", "defaults.seasonFolder", "defaults.region", "defaults.tags",
			}},
			{Title: "Anime", Help: "What an item is moved to once its metadata shows it is anime; no profile, no detection.", When: when("kind", videoKinds...), Paths: []string{
				"defaults.anime.qualityProfileRef", "defaults.anime.seriesType",
			}, Advanced: true},
			{Title: "Naming", Paths: []string{
				"naming.dialect", "naming.colonReplacement", "naming.multiEpisodeStyle", "naming.overrides", "naming.renameFiles",
				"naming.renameTranscoded", "naming.organizeFolders", "naming.writeNFO",
			}, Advanced: true},
			{Title: "Recycle bin", Paths: []string{"recycleBin.path", "recycleBin.cleanupDays"}, Advanced: true},
			{Title: "Permissions", Paths: []string{"permissions.fileMode", "permissions.dirMode", "permissions.group"}, Advanced: true},
		},
		Labels: map[string]string{
			"scanSchedule": "Scan schedule (cron)", "minFreeBytes": "Minimum free bytes", "naming.overrides": "Naming token overrides",
			"naming.renameFiles":               "Rename files to their canonical names",
			"naming.renameTranscoded":          "Rename transcoded files (in their own folder)",
			"naming.organizeFolders":           "Move renamed files into their canonical folders",
			"naming.writeNFO":                  "Write NFO files for Plex",
			"defaults.region":                  "Audible region",
			"defaults.anime.qualityProfileRef": "Anime quality profile", "defaults.anime.seriesType": "Anime series type",
		},
		Refs: map[string]Ref{
			"defaults.qualityProfileRef": RefQualityProfiles, "defaults.transcodeProfileRef": RefTranscodeProfiles,
			"defaults.subtitleProfileRef": RefSubtitleProfiles, "defaults.delayProfileRef": RefDelayProfiles,
			"defaults.anime.qualityProfileRef": RefQualityProfiles,
		},
		// What a root folder's kind makes meaningless is not drawn for it.
		Only: map[string]*When{
			"defaults.minimumAvailability": when("kind", "movie"),
			"defaults.seriesType":          when("kind", "series"),
			"defaults.seasonFolder":        when("kind", "series"),
			"defaults.anime.seriesType":    when("kind", "series"),
			"defaults.monitorNewItems":     when("kind", "series", "music", "book", "comic"),
			"defaults.transcodeProfileRef": when("kind", videoKinds...),
			"defaults.subtitleProfileRef":  when("kind", videoKinds...),
			"defaults.region":              when("kind", "audiobook"),
			"naming.multiEpisodeStyle":     when("kind", "series"),
			"naming.renameTranscoded":      when("kind", videoKinds...),
			"naming.organizeFolders":       when("kind", "music", "book", "audiobook", "comic"),
			"naming.writeNFO":              when("kind", "music", "audiobook"),
		},
		RefOptions: map[string]func(string) *When{
			"defaults.qualityProfileRef":       profileForRoot,
			"defaults.anime.qualityProfileRef": profileForRoot,
		},
		ReadOnlyOnEdit: []string{"path", "kind"},
		// The anime series type alone is no anime defaults (the CRD wants
		// its profile): without a profile there is no detection.
		Ensure: func(spec map[string]any) {
			d, _ := spec["defaults"].(map[string]any)
			a, _ := d["anime"].(map[string]any)
			if a == nil {
				return
			}
			if p, _ := a["qualityProfileRef"].(string); p == "" {
				delete(d, "anime")
			}
		},
	},
	{
		ConfigKind: configKind("qualityprofiles"),
		Title:      "Quality profiles", Singular: "quality profile",
		Help: "Tiers of qualities, best first, and the cutoff tier that stops upgrades. Built-in TRaSH profiles cannot be edited; copy one to change it. Custom-format scoring is TRaSH's and not editable here.",
		Groups: []Group{
			{Title: "Profile", Paths: []string{"mediaKind", "cutoff", "upgradeAllowed", "language", "preferredProtocol", "preferSmallest", "properPolicy", "sizeTable", "scoreSet"}},
			{Title: "Tiers", Help: "Best first. Each tier lists the quality names it accepts, one per line. A last-resort tier, below the cutoff, is taken only when a search finds no release of a better tier; a file of it is replaced by one, even after a transcode.", Paths: []string{"tiers"}},
			{Title: "Custom formats", Paths: []string{"minFormatScore", "cutoffFormatScore", "minUpgradeFormatScore", "enabledFormatGroups"}, Advanced: true},
			{Title: "Size and bitrate limits", Paths: []string{"sizeLimits", "bitrateLimits"}, Advanced: true},
			{Title: "Audio", When: when("mediaKind", "video"), Help: "Audio languages a file must carry (BCP-47 tags, or original for the item's own language), one per line; when set it replaces Language. The default, if set, must be one of them. Graft completes a file missing one with another release's audio.", Paths: []string{"audio.languages", "audio.graft", "audio.default", "audio.completeFirst"}, Advanced: true},
		},
		// The TRaSH tables, score sets and format families are video's.
		Only: map[string]*When{
			"sizeTable":           when("mediaKind", "video"),
			"scoreSet":            when("mediaKind", "video"),
			"enabledFormatGroups": when("mediaKind", "video"),
			"bitrateLimits":       when("mediaKind", "video"),
		},
		Checks:         []string{"enabledFormatGroups"},
		Hidden:         []string{"builtIn", "seedHash", "formatScores"},
		Labels:         map[string]string{"cutoff": "Cutoff tier", "scoreSet": "Score set", "sizeTable": "Size table", "audio.languages": "Audio languages", "audio.graft": "Graft missing audio", "audio.default": "Default audio language", "audio.completeFirst": "Rank complete audio first", "bitrateLimits": "Bitrate limits (bytes per second)"},
		ReadOnlyOnEdit: []string{"mediaKind"},
		// The graft checkbox always posts, so an untouched Audio group decodes
		// to {graft: false} with no languages, which the CRD refuses: drop it.
		Ensure: func(spec map[string]any) {
			a, ok := spec["audio"].(map[string]any)
			if !ok {
				return
			}
			if l, _ := a["languages"].([]any); len(l) == 0 {
				if ls, _ := a["languages"].([]string); len(ls) == 0 {
					delete(spec, "audio")
				}
			}
		},
	},
	{
		ConfigKind: configKind("metadataproviders"),
		Title:      "Metadata providers", Singular: "metadata provider",
		Help: "A source of titles, artwork and ratings. The type cannot change once created. The metadata gateway reads providers at start, so a new or changed provider is used after the metadata agent restarts.",
		Groups: []Group{
			{Title: "Provider", Paths: []string{"type", "enabled", "priority", "baseURL", "language", "region", "contactUserAgent"}},
			{Title: "Credentials", When: when("type", "tmdb", "tvdb", "comicvine", "fanart", "mdblist", "omdb", "theintrodb", "hardcover", "metron", "plex", "mediux"), Paths: []string{"secretRef"}},
			{Title: "MediUX", Help: "How an item's image sets are chosen.", When: when("type", "mediux"), Paths: []string{"mediux.preferredCreators", "mediux.languages", "mediux.quality", "mediux.refresh"}},
			{Title: "Rate limit", Paths: []string{"rateLimit.requestsPerSecond", "rateLimit.burst", "rateLimit.perDay"}, Advanced: true},
			{Title: "Cache", Paths: []string{"cacheTTL"}, Advanced: true},
		},
		Labels: map[string]string{
			"contactUserAgent": "Contact user agent (required for musicbrainz, openlibrary)", "rateLimit.perDay": "Requests per day",
			"mediux.preferredCreators": "Preferred creators (one per line)", "mediux.languages": "Image languages (one per line)",
			"mediux.quality": "Image quality", "mediux.refresh": "Reconsider every",
		},
		Secrets: []Secret{{Path: "secretRef", Keys: []Key{
			{Key: "apiKey", Label: "API key", When: when("type", "tmdb", "tvdb", "comicvine", "fanart", "mdblist", "omdb", "theintrodb")},
			{Key: "apiKeySecondary", Label: "Second API key", Help: "Spent once the first key's daily quota is out.", When: when("type", "mdblist")},
			{Key: "pin", Label: "PIN", Help: "TVDB subscriber PIN, if any.", When: when("type", "tvdb")},
			{Key: "bearer", Label: "Bearer token", When: when("type", "hardcover", "metron")},
			{Key: "token", Label: "Token", Help: "A Plex account token, or a MediUX bearer token.", When: when("type", "plex", "mediux")},
		}}},
		// A region and a language are read only by the providers that
		// localise their answers.
		Only: map[string]*When{
			"region":   when("type", "tmdb", "tvdb", "audible", "audnexus"),
			"language": unless("type", "coverart", "fanart", "animelists", "theintrodb", "mdblist", "omdb", "plex"),
		},
		ReadOnlyOnEdit: []string{"type"},
	},
	{
		ConfigKind: configKind("importlists"),
		Title:      "Import lists", Singular: "import list",
		Help: "A list whose titles are added to the library on every refresh: a Trakt or Plex watchlist, a TMDB, MDBList, StevenLu, IMDb CSV, custom or *arr list. The provider cannot change once created. A Trakt list needs authorizing after it is created: the Import Lists page shows the device code while that is pending.",
		Choice: &Choice{
			Name: "provider", Label: "Provider", Help: "Where the list comes from.",
			Options: []Option{
				{Value: "trakt", Label: "Trakt"},
				{Value: "plex", Label: "Plex watchlist"},
				{Value: "tmdb", Label: "TMDB list or discover"},
				{Value: "mdblist", Label: "MDBList"},
				{Value: "stevenLu", Label: "StevenLu popular movies"},
				{Value: "imdbCSV", Label: "IMDb CSV (from a ConfigMap)"},
				{Value: "custom", Label: "Custom URL"},
				{Value: "arr", Label: "Radarr, Sonarr or another Clustarr"},
			},
			ReadOnlyOnEdit: true,
			Current:        importListProvider,
			Shape:          shapeImportListProvider,
		},
		Groups: []Group{
			{Title: "List", Paths: []string{"kinds", "enabled", "refreshInterval", "syncLevel", "automaticAdd"}},
			{Title: "Trakt", When: whenProvider("trakt"), Paths: []string{"trakt.listType", "trakt.username", "trakt.listSlug", "trakt.limit"}},
			{Title: "TMDB", When: whenProvider("tmdb"), Paths: []string{"tmdb.listID", "tmdb.discover"}},
			{Title: "MDBList", When: whenProvider("mdblist"), Paths: []string{"mdblist.url"}},
			{Title: "IMDb CSV", When: whenProvider("imdbCSV"), Paths: []string{"imdbCSV.configMapRef.name"}},
			{Title: "Custom", When: whenProvider("custom"), Paths: []string{"custom.url", "custom.format"}},
			{Title: "Arr", When: whenProvider("arr"), Paths: []string{"arr.baseURL", "arr.kind"}},
			{Title: "Credentials", When: whenProvider("trakt", "plex", "mdblist"), Paths: []string{"secretRef"}},
			{Title: "Defaults", Help: "What an added item gets.", Paths: []string{
				"defaults.qualityProfileRef", "defaults.rootFolderRef", "defaults.delayProfileRef", "defaults.monitored", "defaults.monitorNewItems",
				"defaults.minimumAvailability", "defaults.seriesType", "defaults.seasonFolder", "defaults.searchOnAdd", "defaults.tags",
			}},
			{Title: "Listed items", Advanced: true, Help: "What every item the list names carries, whoever added it, until the list no longer names it. A tag is the item's Plex label, so a smart collection filtering on it gathers the list.", Paths: []string{
				"listed.tags", "listed.labels", "listed.annotations",
			}},
		},
		Checks: []string{"kinds"},
		// What a provider can yield (the CRD's own rules), and what each
		// kind of item takes.
		OptionsWhen: map[string]map[string]*When{"kinds": {
			"movie":     unless("arr.kind", "sonarr", "lidarr", "readarr"),
			"series":    and(unless("__choice.provider", "stevenLu"), unless("arr.kind", "radarr", "lidarr", "readarr")),
			"album":     and(whenProvider("custom", "arr"), unless("arr.kind", "radarr", "sonarr", "readarr")),
			"book":      and(whenProvider("custom", "arr"), unless("arr.kind", "radarr", "sonarr", "lidarr")),
			"audiobook": and(whenProvider("custom", "arr"), unless("arr.kind", "radarr", "sonarr", "lidarr")),
			"comic":     and(whenProvider("custom", "arr"), unless("arr.kind", "radarr", "sonarr", "lidarr", "readarr")),
		}},
		Only: map[string]*When{
			"trakt.username":               when("trakt.listType", "watchlist", "list", "collection"),
			"trakt.listSlug":               when("trakt.listType", "list"),
			"defaults.minimumAvailability": when("kinds", "movie"),
			"defaults.seriesType":          when("kinds", "series"),
			"defaults.seasonFolder":        when("kinds", "series"),
			"defaults.monitorNewItems":     when("kinds", "series", "album", "book", "comic"),
		},
		RefOptions: map[string]func(string) *When{
			"defaults.qualityProfileRef": profileForList,
			"defaults.rootFolderRef":     rootForList,
		},
		Labels: map[string]string{
			"kinds": "Kinds", "imdbCSV.configMapRef.name": "ConfigMap name",
			"tmdb.listID": "List ID", "tmdb.discover": "Discover parameters", "mdblist.url": "List URL", "custom.url": "List URL", "arr.baseURL": "Base URL",
		},
		Refs: map[string]Ref{"defaults.qualityProfileRef": RefQualityProfiles, "defaults.rootFolderRef": RefRootFolders, "defaults.delayProfileRef": RefDelayProfiles},
		Secrets: []Secret{{Path: "secretRef", Keys: []Key{
			{Key: "clientID", Label: "Client ID", When: whenProvider("trakt", "plex")},
			{Key: "clientSecret", Label: "Client secret", When: whenProvider("trakt")},
			{Key: "token", Label: "Plex token", When: whenProvider("plex")},
			{Key: "apiKey", Label: "API key", When: whenProvider("mdblist")},
		}}},
	},
	{
		ConfigKind: configKind("indexers"),
		Title:      "Indexers", Singular: "indexer",
		Help: "A tracker or Newznab/Torznab server. Pick a bundled Cardigann definition, or leave it blank for a generic Newznab/Torznab upstream.",
		Groups: []Group{
			{Title: "Indexer", Paths: []string{"definition", "baseURL", "enabled", "priority", "downloadClientRef", "proxyRef", "tags"}},
			{Title: "Generic Newznab/Torznab", Help: "For an upstream with no definition.", Paths: []string{"generic.protocol", "generic.apiPath"}, When: when("definition", "")},
			{Title: "Credentials", Help: "The Secret the indexer's credentials are kept in: a generic upstream's API key here, a definition's own under Definition.", Paths: []string{"secretRef"}},
			{Title: "Search", Paths: []string{"enableRss", "enableAutomaticSearch", "enableInteractiveSearch", "rssInterval", "categories", "animeCategories", "animeOnly", "animeStandardFormatSearch", "minimumSeeders"}},
			{Title: "Limits", Paths: []string{"limits.queryLimit", "limits.grabLimit", "limits.unit", "requestDelay", "timeout"}, Advanced: true},
			{Title: "Seeding", When: indexerNot("usenet"), Paths: []string{"seedCriteria"}, Advanced: true},
			{Title: "Definition object", Help: "An IndexerDefinition by its object name, in place of a definition's id.", When: when("definition", ""), Paths: []string{"definitionRef"}, Advanced: true},
		},
		// The settings are the definition's: the section it fills draws them.
		External: []string{"settings"},
		// Seeders are a torrent's, and a grab goes to a client of the
		// indexer's own protocol.
		Only: map[string]*When{"minimumSeeders": indexerNot("usenet")},
		RefOptions: map[string]func(string) *When{"downloadClientRef": func(protocol string) *When {
			switch protocol {
			case "torrent":
				return indexerNot("usenet")
			case "usenet":
				return indexerNot("torrent")
			}
			return nil
		}},
		Labels: map[string]string{
			"definition": "Cardigann definition", "definitionRef": "IndexerDefinition object", "enableRss": "Enable RSS",
			"enableAutomaticSearch": "Enable automatic search", "enableInteractiveSearch": "Enable interactive search",
			"rssInterval": "RSS interval", "categories": "Categories (Newznab ids)", "animeCategories": "Anime categories (Newznab ids)",
			"settings": "Definition settings", "limits.unit": "Limit window",
		},
		Refs: map[string]Ref{"definition": RefIndexerDefinitions, "downloadClientRef": RefDownloadClients, "proxyRef": RefIndexerProxies},
		// A definition's credentials are its own settings, drawn with them.
		Secrets: []Secret{{Path: "secretRef", Help: "The Secret the credentials are written to, each under its setting's name (apikey for a generic upstream); created or updated from them. Blank names it after this indexer.", Keys: []Key{
			{Key: "apikey", Label: "API key", When: when("definition", "")},
		}}},
		Ensure: func(spec map[string]any) {
			if d, _ := spec["definition"].(string); d != "" {
				delete(spec, "generic")
				delete(spec, "definitionRef")
			}
		},
	},
	{
		ConfigKind: configKind("indexerproxies"),
		Title:      "Indexer proxies", Singular: "indexer proxy",
		Help: "An HTTP, SOCKS or FlareSolverr proxy indexer traffic goes through. Attach it by choosing it as an indexer's Proxy.",
		Groups: []Group{
			{Title: "Proxy", Paths: []string{"type", "host", "port", "requestTimeout", "secretRef"}},
		},
		Hidden: []string{"selector"},
		Labels: map[string]string{"requestTimeout": "Request timeout", "secretRef": "Authentication"},
		Secrets: []Secret{{Path: "secretRef", Keys: []Key{
			{Key: "username", Label: "Username", Help: "A SOCKS4 proxy takes it as its user id."},
			{Key: "password", Label: "Password", When: when("type", "http", "socks5")},
		}}},
		// FlareSolverr takes no credentials.
		Only: map[string]*When{"secretRef": when("type", "http", "socks4", "socks5")},
	},
	{
		ConfigKind: configKind("downloadclients"),
		Title:      "Download clients", Singular: "download client",
		Help: "An embedded torrent or usenet engine the grab controller runs for you. The protocol cannot change once created.",
		Groups: []Group{
			{Title: "Client", Paths: []string{"protocol", "enabled", "priority", "categories"}},
			{Title: "Torrent", When: whenTorrent, Paths: []string{
				"torrent.listenPort", "torrent.publicIP", "torrent.maxActive", "torrent.maxSeeding", "torrent.downloadLimitBps", "torrent.uploadLimitBps",
				"torrent.enableDHT", "torrent.enablePEX", "torrent.removeCompleted", "torrent.stallTimeout", "torrent.maxUnverifiedBytes", "torrent.seed",
			}},
			{Title: "Storage", When: whenTorrent, Help: "Keep incomplete torrents in a working area and move each to the publish directory when complete; with no working area a torrent downloads and seeds in the publish directory.", Paths: []string{
				"torrent.publishDir", "torrent.scratch",
			}},
			{Title: "Proxy", When: whenTorrent, Paths: []string{
				"torrent.proxy.type", "torrent.proxy.host", "torrent.proxy.port", "torrent.proxy.secretRef",
				"torrent.proxy.hostnameLookup", "torrent.proxy.dnsServer", "torrent.proxy.peerConnections", "torrent.proxy.udp",
			}},
			{Title: "Usenet", When: whenUsenet, Paths: []string{
				"usenet.providers", "usenet.postProcess", "usenet.propagationDelay", "usenet.preCheck", "usenet.abortHealthPercent",
				"usenet.healthAction", "usenet.downloadTimeout", "usenet.stallTimeout", "usenet.publishDir", "usenet.scratch",
				"usenet.maxActive", "usenet.maxPostProcess",
			}},
			{Title: "Engine", When: whenTorrent, Help: "A usenet client runs one engine.", Paths: []string{"replicas"}, Advanced: true},
		},
		Hidden: []string{"resources", "nodeSelector", "tolerations"},
		Labels: map[string]string{
			"categories": "Category folders (kind → subdirectory)", "torrent.listenPort": "Listen port", "torrent.publicIP": "Public IP",
			"torrent.downloadLimitBps": "Download limit (bytes/s)", "torrent.uploadLimitBps": "Upload limit (bytes/s)",
			"usenet.providers": "Servers", "usenet.providers[].quotaBytes": "Monthly quota (bytes)", "usenet.providers[].backup": "Backup server",
			"usenet.abortHealthPercent": "Abort below health (%)", "usenet.scratch.sizeLimit": "Scratch size",
			"usenet.publishDir": "Publish directory", "usenet.scratch.path": "Scratch path (on the data volume)",
			"torrent.publishDir": "Publish directory", "torrent.scratch.path": "Scratch path (on the data volume)",
			"torrent.scratch.sizeLimit": "Scratch size", "torrent.scratch.existingClaim": "Existing claim",
			"torrent.scratch.volumeName":   "Persistent volume",
			"usenet.scratch.existingClaim": "Existing claim", "usenet.scratch.volumeName": "Persistent volume",
			"usenet.stallTimeout": "Stall timeout (no article completed)",
			"usenet.maxActive":    "Downloads at once", "usenet.maxPostProcess": "Repairs and unpacks at once",
			"torrent.maxSeeding": "Seeding at once (0 for no limit)",
			"torrent.proxy.type": "Type", "torrent.proxy.host": "Host", "torrent.proxy.port": "Port",
			"torrent.proxy.secretRef":       "Authentication",
			"torrent.proxy.hostnameLookup":  "Perform hostname lookup via proxy",
			"torrent.proxy.dnsServer":       "DNS server (queried through the proxy)",
			"torrent.proxy.peerConnections": "Use proxy for peer connections",
			"torrent.proxy.udp":             "Proxy UDP (DHT, uTP, UDP trackers)",
		},
		Secrets: []Secret{{Path: "usenet.providers[].secretRef", Keys: []Key{
			{Key: "username", Label: "Username", Required: true},
			{Key: "password", Label: "Password", Required: true},
		}}, {Path: "torrent.proxy.secretRef", Keys: []Key{
			{Key: "username", Label: "Username"},
			{Key: "password", Label: "Password"},
		}}},
		ReadOnlyOnEdit: []string{"protocol"},
		Ensure: func(spec map[string]any) {
			switch spec["protocol"] {
			case "torrent":
				delete(spec, "usenet")
				if _, ok := spec["torrent"]; !ok {
					spec["torrent"] = map[string]any{}
				}
				// The proxy's switches post true on every save, so a client
				// without a proxy decodes one with no host, which the CRD
				// refuses; no host is no proxy, and clearing it removes one.
				if t, ok := spec["torrent"].(map[string]any); ok {
					if p, ok := t["proxy"].(map[string]any); ok && p["host"] == nil {
						delete(t, "proxy")
					}
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
		Help: "A subtitle source the caption service searches. The type cannot change once created.",
		Groups: []Group{
			{Title: "Provider", Paths: []string{"type", "enabled", "priority", "endpoint", "languages", "requestsPerSecondMilli"}},
			{Title: "Credentials", When: when("type", "opensubtitlescom", "subdl", "subsource"), Paths: []string{"secretRef"}},
			{Title: "Options", Help: "aiTranslated (exclude or include), trustedSources (true).", When: unless("type", "embedded", "ocr"), Paths: []string{"options"}, Advanced: true},
		},
		// A file's own tracks have no address and are never paced.
		Only: map[string]*When{
			"endpoint":               unless("type", "embedded", "ocr"),
			"requestsPerSecondMilli": unless("type", "embedded", "ocr"),
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
		Help: "Which files the transcode controller transcodes to its fixed HEVC standard, and where. Changing the quality, container, audio languages or never-transcode modifiers plans files not yet transcoded under the new settings; a transcoded file is never encoded again.",
		Groups: []Group{
			{Title: "Profile", Paths: []string{"default", "priority", "maxConcurrent", "hardware", "container", "quality", "selector.matchLabels"}},
			{Title: "Audio and subtitles", Paths: []string{"audio.languages", "subtitles.languages"}},
			{Title: "Policy", Paths: []string{"policy"}, Advanced: true},
			{Title: "Resources", Paths: []string{"resources.limits", "resources.requests", "scratch", "activeDeadline", "gpu.count", "gpu.runtimeClassName"}, Advanced: true},
		},
		Hidden: []string{"chunking", "ttlSecondsAfterFinished", "gpu.nodeSelector", "gpu.tolerations", "resources.claims", "selector.matchExpressions"},
		Labels: map[string]string{"default": "Cluster default", "maxConcurrent": "Max concurrent jobs", "selector.matchLabels": "Applies to files labelled", "audio.languages": "Keep audio languages", "subtitles.languages": "Keep subtitle languages", "policy.keepCodecs": "Keep codecs (never encoded)"},
		Checks: []string{"policy.keepCodecs"},
		// A CPU profile schedules no GPU.
		Only: map[string]*When{"gpu": unless("hardware", "cpu")},
	},
	{
		ConfigKind: configKind("iptvproviders"),
		Title:      "IPTV providers", Singular: "IPTV provider",
		Help: "An IPTV source whose live channels become Plex Live TV: an Xtream Codes account, or a plain M3U playlist URL. An Xtream guide is the panel's xmltv.php unless Guide URL names another XMLTV; an M3U provider has only its Guide URL, or dummy guides. {username} and {password} in a URL take the account's.",
		Groups: []Group{
			{Title: "Provider", Paths: []string{"type", "enabled", "credentialsRef", "tuners", "refresh", "userAgent"}},
			{Title: "Xtream", When: &When{Path: "type", Values: []string{"Xtream"}}, Paths: []string{"server"}},
			{Title: "M3U", When: &When{Path: "type", Values: []string{"M3U"}}, Paths: []string{"playlistURL"}},
			{Title: "Guide", Paths: []string{"guideURL"}},
			{Title: "Plex guide", Paths: []string{"plexGuide.lineup", "plexGuide.enabled", "plexGuide.credentialsRef.name", "plexGuide.credentialsRef.key"}, Advanced: true},
			{Title: "Channels", Paths: []string{"filters", "channelNumberStart"}},
			{Title: "Automatic lineup", Help: "Rules that sort the playlist into categories and number them; until enabled the plan is only shown.", Paths: []string{"lineup.enabled", "lineup.trim", "lineup.exclude", "lineup.categories"}, Advanced: true},
			{Title: "Plex guide grids", Help: "Plex's own grids as a guide source for the channels paired with stations.", Paths: []string{"guideSources.plex.credentialsRef.name", "guideSources.plex.credentialsRef.key", "guideSources.plex.lineups", "guideSources.plex.refresh", "guideSources.plex.rate"}, Advanced: true},
			{Title: "Plex", Paths: []string{"device.friendlyName", "device.id", "dvr.maxChannels", "dvr.single"}, Advanced: true},
			{Title: "Stream", Paths: []string{"stream.buffer", "stream.linger", "stream.language"}, Advanced: true},
		},
		Labels: map[string]string{
			"guideURL": "Guide URL (XMLTV)", "channelNumberStart": "First channel number",
			"dvr.maxChannels": "Channels per Plex DVR", "credentialsRef": "Account", "playlistURL": "Playlist URL (M3U)",
			"plexGuide.lineup": "Plex lineup id", "plexGuide.enabled": "Channels on Plex's guide",
			"plexGuide.credentialsRef.name": "Plex token Secret", "plexGuide.credentialsRef.key": "Plex token key",
			"lineup.enabled": "Lineup numbers and retires channels", "lineup.trim": "Trim from names (one per line)",
			"lineup.exclude": "Leave out (group or name, one per line)", "lineup.categories": "Categories",
			"guideSources.plex.credentialsRef.name": "Plex token Secret", "guideSources.plex.credentialsRef.key": "Plex token key",
			"guideSources.plex.lineups": "Plex lineup ids (one per line)", "guideSources.plex.refresh": "Fetch every",
			"guideSources.plex.rate": "Requests per second", "dvr.single": "One Plex DVR for every block",
		},
		// The lineup numbers its own channels.
		Only: map[string]*When{"channelNumberStart": unless("lineup.enabled", "true")},
		// Not required here: an M3U provider names a Secret only when its
		// URLs need one, and an Xtream provider without both keys reads
		// Valid=False, SecretKeyNotFound.
		Secrets: []Secret{{Path: "credentialsRef", Keys: []Key{
			{Key: "username", Label: "Username"},
			{Key: "password", Label: "Password"},
		}}},
		// The Plex guide's checkbox always posts plexGuide.enabled, which
		// alone is a plexGuide the CRD refuses (lineup and credentialsRef
		// are required): without a lineup there is no Plex guide.
		Ensure: func(spec map[string]any) {
			g, _ := spec["plexGuide"].(map[string]any)
			if l, _ := g["lineup"].(string); l == "" {
				delete(spec, "plexGuide")
			}
			// Likewise the lineup's switch alone, off, is no lineup; and a
			// guide source without its token's Secret is none.
			if l, ok := spec["lineup"].(map[string]any); ok && len(l) == 1 {
				if on, isBool := l["enabled"].(bool); isBool && !on {
					delete(spec, "lineup")
				}
			}
			if gs, ok := spec["guideSources"].(map[string]any); ok {
				p, _ := gs["plex"].(map[string]any)
				ref, _ := p["credentialsRef"].(map[string]any)
				if n, _ := ref["name"].(string); n == "" {
					delete(spec, "guideSources")
				}
			}
		},
	},
	{
		ConfigKind: configKind("iptvchannels"),
		Title:      "IPTV channels", Singular: "channel",
		Help: "One playlist entry in the lineup. The provider and key cannot change once created.",
		Groups: []Group{
			{Title: "Channel", Paths: []string{"providerRef", "key", "active", "number", "name", "group", "logo", "language"}},
			{Title: "Guide", Help: "A guide channel id, or a dummy guide; neither maps it by its tvg-id.", Paths: []string{"epg.channelID", "epg.dummy", "epg.dummyMinutes", "timeshiftMinutes"}},
			{Title: "Plex station", Help: "Overrides the automatic pairing with the provider's Plex lineup: a station, or off Plex.", Paths: []string{"epg.plex.station", "epg.plex.off"}, Advanced: true},
		},
		Labels: map[string]string{
			"providerRef": "Provider", "key": "Playlist key", "epg.channelID": "Guide channel id", "epg.dummy": "Dummy guide", "epg.dummyMinutes": "Dummy block minutes",
			"epg.plex.station": "Station (gridKey)", "epg.plex.off": "Keep off Plex",
		},
		// A dummy guide has a block length and no channel id; a channel
		// kept off Plex has no station.
		Only: map[string]*When{
			"epg.channelID":    unless("epg.dummy", "true"),
			"epg.dummyMinutes": when("epg.dummy", "true"),
			"epg.plex.station": unless("epg.plex.off", "true"),
		},
		ReadOnlyOnEdit: []string{"providerRef", "key"},
		// The category override is the Channels page's (Automatic, Off, a
		// category): the form does not draw it, and a save must carry the
		// stored value, the empty string of "off every DVR" included --
		// without this a save cleared it and turned an Off channel back on.
		Hidden: []string{"category"},
		// The dummy checkbox always posts, so an untouched Guide group
		// decodes to {dummy: false}, which the CRD refuses (exactly one of
		// channelID and dummy): no channel id and no dummy is no epg.
		// The Plex switch posts the same way: a station set means on Plex,
		// neither a station nor off is no override; and an epg left with
		// nothing is none.
		Ensure: func(spec map[string]any) {
			e, ok := spec["epg"].(map[string]any)
			if !ok {
				return
			}
			if p, ok := e["plex"].(map[string]any); ok {
				off, _ := p["off"].(bool)
				station, _ := p["station"].(string)
				switch {
				case off:
					delete(p, "station")
				case station != "":
					delete(p, "off")
				default:
					delete(e, "plex")
				}
			}
			if id, _ := e["channelID"].(string); id != "" {
				delete(e, "dummy")
			} else if d, _ := e["dummy"].(bool); !d {
				delete(e, "dummy")
				delete(e, "dummyMinutes")
			}
			if len(e) == 0 {
				delete(spec, "epg")
			}
		},
	},
}

// whenProvider is a condition on the ImportList form's provider choice.
func whenProvider(values ...string) *When {
	return &When{Path: "__choice.provider", Values: values}
}

// importListProvider is the provider a stored ImportList spec carries:
// the one sub-object of importListProviders that exists.
func importListProvider(spec map[string]any) string {
	for _, p := range importListProviders {
		if _, ok := spec[p]; ok {
			return p
		}
	}
	return ""
}

// shapeImportListProvider shapes a decoded spec to the chosen provider:
// the others are dropped (a hidden section's stale inputs included) and
// the chosen one exists, as the empty object Plex and StevenLu are.
func shapeImportListProvider(spec map[string]any, value string) {
	if !slices.Contains(importListProviders, value) {
		return
	}
	for _, p := range importListProviders {
		if p != value {
			delete(spec, p)
		}
	}
	if _, ok := spec[value].(map[string]any); !ok {
		spec[value] = map[string]any{}
	}
}
