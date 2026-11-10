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

package naming

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation"
)

// Sources: S2', S3', S4' and S11 as the old controller's naming watches, and
// S10' (a QualityProfile's language or audio policy changing: the item's
// configured language, which {MediaInfo AudioLanguages} names against). A
// transcode holds a rename through the file's own status.transcode (F6.2).
// Since 2026-11-10 (design D7, §5.5) the non-video kinds have theirs too:
// an Album and its Artist, a Book and its Author, an Audiobook, an Issue
// and its Comic (nonvideo_sources.go), and S11 fans out to a music, book,
// audiobook or comic root's files beside a movie or series root's.
func (*Adapter) Sources(mgr ctrl.Manager) ([]remediation.Source, error) {
	// The cache itself, not the client over it: the file maps read keys
	// through rowcache.Keys, which the cache answers without decoding.
	c := mgr.GetCache()
	via := func(f func(context.Context, client.Reader, client.Object) []types.NamespacedName) func(context.Context, client.Object) []types.NamespacedName {
		return func(ctx context.Context, o client.Object) []types.NamespacedName { return f(ctx, c, o) }
	}
	return []remediation.Source{
		{
			Name: "S2'/Movie", Object: &catalogv1alpha1.Movie{}, Handler: remediation.MapFiles(via(mediafile.FilesForMovie)),
			Predicates: []predicate.Predicate{mediafile.MovieNamingChanged()},
		},
		{
			Name: "S3'/Episode", Object: &catalogv1alpha1.Episode{}, Handler: remediation.MapFiles(via(mediafile.FilesForEpisode)),
			Predicates: []predicate.Predicate{mediafile.EpisodeNamingChanged()},
		},
		{
			Name: "S4'/Series", Object: &catalogv1alpha1.Series{}, Handler: remediation.MapFiles(via(mediafile.FilesForSeries)),
			Predicates: []predicate.Predicate{mediafile.SeriesNamingChanged()},
		},
		{
			Name: "S11/RootFolder", Object: &catalogv1alpha1.RootFolder{},
			Handler: remediation.MapFiles(func(ctx context.Context, o client.Object) []types.NamespacedName {
				return append(mediafile.FilesForRootFolder(ctx, c, o), nonVideoFilesOfRootFolder(ctx, c, o)...)
			}),
			Predicates: []predicate.Predicate{mediafile.RootFolderNamingChanged()},
		},
		{
			Name: "S12/Album", Object: &catalogv1alpha1.Album{}, Handler: remediation.MapFiles(via(filesOfAlbum)),
			Predicates: []predicate.Predicate{albumNamingChanged()},
		},
		{
			Name: "S12/Artist", Object: &catalogv1alpha1.Artist{}, Handler: remediation.MapFiles(via(filesOfArtist)),
			Predicates: []predicate.Predicate{artistNamingChanged()},
		},
		{
			Name: "S12/Book", Object: &catalogv1alpha1.Book{}, Handler: remediation.MapFiles(via(filesOfBook)),
			Predicates: []predicate.Predicate{bookNamingChanged()},
		},
		{
			Name: "S12/Author", Object: &catalogv1alpha1.Author{}, Handler: remediation.MapFiles(via(filesOfAuthor)),
			Predicates: []predicate.Predicate{authorNamingChanged()},
		},
		{
			Name: "S12/Audiobook", Object: &catalogv1alpha1.Audiobook{}, Handler: remediation.MapFiles(via(filesOfAudiobook)),
			Predicates: []predicate.Predicate{audiobookNamingChanged()},
		},
		{
			Name: "S12/Issue", Object: &catalogv1alpha1.Issue{}, Handler: remediation.MapFiles(via(filesOfIssue)),
			Predicates: []predicate.Predicate{issueNamingChanged()},
		},
		{
			Name: "S12/Comic", Object: &catalogv1alpha1.Comic{}, Handler: remediation.MapFiles(via(filesOfComic)),
			Predicates: []predicate.Predicate{comicNamingChanged()},
		},
		remediation.QualityProfileSource(c),
	}, nil
}
