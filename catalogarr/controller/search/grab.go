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

package search

import (
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// BuildDownloadSource maps a release to a Download's spec.source. A magnet
// link needs no indexer round-trip and is preferred when present; otherwise
// the release is resolved through indexarr (rpc.indexarr.download) via
// IndexerDownload, which applies the indexer's own auth, rate limits and
// proxying -- spec §8.2's "source (indexerDownload when the indexer is
// authenticated)".
//
// DownloadSource's CEL rule requires exactly one member, so this function
// never sets two. torrentURL/nzbURL are deliberately not produced: a bare
// DownloadURL on a release is the indexer's own link, which usually carries
// the indexer's API key, and the whole point of IndexerDownload is to keep
// that credential out of the Download object.
//
// Exported because the automatic-grab path (out of this task's scope) needs
// the identical mapping.
func BuildDownloadSource(rel commonv1.ReleaseInfo) downloadv1alpha1.DownloadSource {
	if rel.MagnetURL != "" {
		m := rel.MagnetURL
		return downloadv1alpha1.DownloadSource{MagnetURL: &m}
	}
	return downloadv1alpha1.DownloadSource{
		IndexerDownload: &downloadv1alpha1.IndexerDownload{
			IndexerRef: rel.IndexerRef,
			GUID:       rel.GUID,
			URL:        rel.DownloadURL,
		},
	}
}

// toDownloadSourceAC converts the plain value BuildDownloadSource returns into
// the apply configuration k8s.Apply needs. It is a field-by-field copy rather
// than a marshal/unmarshal round trip so a new member added to DownloadSource
// fails to compile here instead of being silently dropped on the wire.
func toDownloadSourceAC(src downloadv1alpha1.DownloadSource) *downloadac.DownloadSourceApplyConfiguration {
	ac := downloadac.DownloadSource()
	if src.MagnetURL != nil {
		ac = ac.WithMagnetURL(*src.MagnetURL)
	}
	if src.TorrentURL != nil {
		ac = ac.WithTorrentURL(*src.TorrentURL)
	}
	if src.NZBURL != nil {
		ac = ac.WithNZBURL(*src.NZBURL)
	}
	if src.IndexerDownload != nil {
		ac = ac.WithIndexerDownload(downloadac.IndexerDownload().
			WithIndexerRef(src.IndexerDownload.IndexerRef).
			WithGUID(src.IndexerDownload.GUID).
			WithURL(src.IndexerDownload.URL))
	}
	if src.ExpectedInfoHash != nil {
		ac = ac.WithExpectedInfoHash(*src.ExpectedInfoHash)
	}
	return ac
}

// grabDecision is resolveGrab's verdict for one requested GUID.
type grabDecision struct {
	Release commonv1.ReleaseInfo
	Allowed bool
	Error   string
}

// resolveGrab looks guid up in results and applies spec §8.2's "Override
// required for Permanent rejections" rule: an approved release, or a rejected
// one whose Rejections are all temporary, grabs freely; a release carrying at
// least one Permanent rejection needs spec.override.
//
// A temporary rejection is deliberately not a barrier. Temporary means "this
// may pass on a later run" -- the queue already holds an equal candidate, the
// item is not available yet -- and a human looking at status.results and
// picking that release is making exactly the judgement call the temporary
// rejection was deferring.
func resolveGrab(guid string, results []commonv1.ReleaseDecision, override bool) grabDecision {
	for _, r := range results {
		if r.GUID != guid {
			continue
		}
		if r.Approved {
			return grabDecision{Release: r.ReleaseInfo, Allowed: true}
		}
		for _, rej := range r.Rejections {
			if rej.Type == commonv1.RejectionPermanent && !override {
				return grabDecision{
					Release: r.ReleaseInfo,
					Error:   "release was permanently rejected: " + rej.Reason + "; set spec.override to grab it anyway",
				}
			}
		}
		return grabDecision{Release: r.ReleaseInfo, Allowed: true}
	}
	return grabDecision{Error: "guid " + guid + " is not in status.results"}
}
