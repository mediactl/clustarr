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

package logging

// Subsystems is every name clustarr passes to Subsystem or SubsystemLogger,
// sorted (the 2026-11-10 logging design §3, §5); test/guards holds the
// module to it. A unit's name is its path below app/ with "/" read as "."
// and the segments controller, worker, agent and manager dropped
// (app/livetv/tuner is livetv.tuner, app/catalog/worker/search is
// catalog.search), app/indexer being the index domain; ui and ui/plex are
// ui and ui.plex; a pkg/ package with an entry point of its own takes its
// service's name (pkg/events/natsbus is events.nats). The remediation
// loop's planners and item stages are remediation.<name>. The rest are the
// third-party writers' (design §4): controller-runtime, client-go, stdlog
// (the log package and slog.Default), ffmpeg, onnx and this package's own
// logging.
var Subsystems = []string{
	"autoscale",
	"autoscale.extmetrics",
	"caption.fetch",
	"caption.subtitleprofile",
	"caption.subtitleprovider",
	"catalog.artwork",
	"catalog.artwork.reaper",
	"catalog.backlogsearch",
	"catalog.delayprofile",
	"catalog.history",
	"catalog.history.dlq",
	"catalog.history.replay",
	"catalog.imagesets",
	"catalog.markers",
	"catalog.metadata",
	"catalog.metadata.fetch",
	"catalog.metadataprovider",
	"catalog.metadatarefresh",
	"catalog.overlayprofile",
	"catalog.qualityprofile",
	"catalog.rootfolder",
	"catalog.rssmatcher",
	"catalog.search",
	"catalog.segmentplan",
	"catalog.wantedcron",
	"client-go",
	"controller-runtime",
	"dispatch.ledger",
	"events.nats",
	"ffmpeg",
	"grab.downloadclient",
	"grab.engine",
	"grab.engine.progress",
	"grab.engine.report",
	"grab.torrent.anacrolix",
	"import.fileimport",
	"import.importexclusion",
	"import.importlist",
	"import.librarydelete",
	"import.libraryscan",
	"import.nfo",
	"import.probe",
	"import.recycle",
	"import.recyclesweep",
	"import.rescan",
	"import.rootfolderschedule",
	"index.facade",
	"index.indexer",
	"index.indexerdefinition",
	"index.indexerproxy",
	"index.rss",
	"index.sweeper",
	"intake.advisory",
	"intake.candidates",
	"intake.scans",
	"livetv.iptvchannel",
	"livetv.iptvprovider",
	"livetv.leaderendpoint",
	"livetv.lineup",
	"livetv.tuner",
	"logging",
	"maintenance.objectstores",
	"markers",
	"onnx",
	"remediation",
	"remediation.adopt",
	"remediation.artwork",
	"remediation.backlog",
	"remediation.downloads",
	"remediation.grab",
	"remediation.graft",
	"remediation.imageset",
	"remediation.items",
	"remediation.markers",
	"remediation.metadata",
	"remediation.naming",
	"remediation.nfo",
	"remediation.overlay",
	"remediation.probe",
	"remediation.search",
	"remediation.subtitles",
	"remediation.transcode",
	"stdlog",
	"transcode",
	"transcode.admission",
	"transcode.admission.intelwork",
	"transcode.graft",
	"transcode.transcodeprofile",
	"ui",
}
