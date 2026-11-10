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

package nfo

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/nfo"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
	"github.com/mediactl/clustarr/pkg/version"
)

// Durable is the import agent's consumer the stage dispatches to.
const Durable = events.ConsumerImportNFO

// albumByArtistRef is the manager's Album index by spec.artistRef
// (artist.RegisterIndexes, through remediation.RegisterIndexes).
const albumByArtistRef = ".spec.artistRef"

// Options is what the stage reads through.
type Options struct {
	Reader   client.Reader
	Bus      events.Bus
	Dispatch *dispatch.Ledger
}

// Stage is the nfo stage (remediation.ItemStage, SetDeclarer and Watcher).
type Stage struct {
	o       Options
	records *records.Requester[*schema.NFORecord]
}

var (
	_ remediation.ItemStage   = (*Stage)(nil)
	_ remediation.SetDeclarer = (*Stage)(nil)
	_ remediation.Watcher     = (*Stage)(nil)
)

// New builds the stage.
func New(o Options) *Stage {
	s := &Stage{o: o}
	if o.Bus != nil {
		s.records = records.NewRequester(o.Bus.KV(events.BucketNFO), agentrecords.NFO())
	}
	return s
}

// Name implements remediation.ItemStage.
func (s *Stage) Name() remediation.ItemStageName { return remediation.StageNFO }

// Applies implements remediation.ItemStage: the four kinds of design §8.1
// -- an Artist and its Albums, and for an audiobook root an Author rendered
// as the artist of its Audiobooks.
func (s *Stage) Applies(k remediation.KeyKind) bool { return applies(k) }

func applies(k remediation.KeyKind) bool {
	switch k {
	case remediation.KindArtist, remediation.KindAlbum, remediation.KindAuthor, remediation.KindAudiobook:
		return true
	}
	return false
}

// DeclaresSets implements remediation.SetDeclarer: status.nfo under
// catalog-nfo.
func (s *Stage) DeclaresSets(k remediation.KeyKind) []k8s.FieldManager {
	if !applies(k) {
		return nil
	}
	return []k8s.FieldManager{k8s.ManagerCatalogNFO}
}

// Plan implements remediation.ItemStage.
func (s *Stage) Plan(ctx context.Context, _ *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	var res remediation.ItemResult
	if v.Item == nil || v.Item.GetDeletionTimestamp() != nil || s.records == nil {
		return res, nil
	}
	in, err := s.gather(ctx, v)
	if err != nil {
		return res, remediation.Transient(err)
	}
	if in == nil {
		return res, nil
	}
	uid := string(v.Item.GetUID())
	rec, rev, ok, err := s.records.Get(ctx, agentrecords.NFOKey(uid))
	if err != nil {
		return res, remediation.Transient(err)
	}
	in.Record, in.RecordRev, in.RecordOK = rec, rev, ok
	d := Plan(*in)
	res.Due = d.Due
	key := remediation.ItemLedgerKey(v.Key)
	if d.Status != nil {
		res.Sets = []itempass.Set{{Manager: k8s.ManagerCatalogNFO, Fields: map[string]any{"nfo": d.Status}}}
	}
	if d.Answered {
		res.Effects = append(res.Effects, remediation.DispatchAnswered{Durable: Durable, Ledger: key, Seq: in.Record.Seq})
	}
	if d.Request != nil {
		if s.o.Dispatch != nil {
			if dec := s.o.Dispatch.Admit(Durable, key, d.Request.Seq); !dec.Admitted {
				s.o.Dispatch.Waiting(Durable, key, dec.Reason)
				res.Due = v.Now.Add(max(dec.RetryAfter, time.Second))
				return res, nil
			}
		}
		value, err := json.Marshal(Record(d.Request))
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.RecordWrite{
			Bucket: events.BucketNFO, Key: agentrecords.NFOKey(uid),
			Revision: in.RecordRev, Value: value, Remediation: "nfo", Lane: "none",
		})
		subject, id, env, err := TaskMessage(d.Request, v.Now.Time)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.DispatchPublish{
			Publish: remediation.Publish{Subject: subject, MsgID: id, ExpectStream: events.StreamWorkImport, Envelope: env},
			Durable: Durable, Ledger: key, Seq: d.Request.Seq,
		})
	}
	return res, nil
}

// TaskMessage is the subject, Msg-Id and envelope of t: one write per item
// and seq.
func TaskMessage(t *schema.NFOWriteTask, now time.Time) (subject, msgID string, env *events.Envelope, err error) {
	name, data, err := schema.Encode(*t)
	if err != nil {
		return "", "", nil, err
	}
	id := fmt.Sprintf("nfo/%s/%d", t.Item.UID, t.Seq)
	env = &events.Envelope{
		ID: id, Type: "import.NFOWriteTask", Schema: name, Source: "catalog@" + version.String(),
		Key: t.Item.Namespace + "/" + t.Item.Name, Time: now.UTC(), Data: data,
	}
	return events.WorkNFOSubject(t.Item.Kind, t.Item.UID), id, env, nil
}

// gather reads what Plan decides from; nil when the item has nothing to
// write yet (no folder, no metadata, no root, or the root's writeNFO off).
func (s *Stage) gather(ctx context.Context, v *remediation.ItemView) (*Input, error) {
	in := &Input{Item: metadatainputs.ItemRef(v.Item), Now: v.Now.Time}
	var rootRef string
	switch item := v.Item.(type) {
	case *catalogv1alpha1.Artist:
		if item.Status.Metadata == nil || item.Status.Path == "" {
			return nil, nil
		}
		rootRef = item.Spec.RootFolderRef
		in.Folder, in.Prev = item.Status.Path, item.Status.NFO
		var albums catalogv1alpha1.AlbumList
		if err := s.o.Reader.List(ctx, &albums, client.InNamespace(item.Namespace), client.MatchingFields{albumByArtistRef: item.Name}); err != nil {
			return nil, err
		}
		in.Rendered = renderArtist(item, orderedAlbums(albums.Items))
	case *catalogv1alpha1.Author:
		// Only under an audiobook root, which Enabled decides below: a
		// book root gets no artist.nfo (design §8.1).
		if item.Status.Metadata == nil || item.Status.Path == "" {
			return nil, nil
		}
		rootRef = item.Spec.RootFolderRef
		in.Folder, in.Prev = item.Status.Path, item.Status.NFO
		audiobooks, err := s.audiobooksOf(ctx, item)
		if err != nil {
			return nil, err
		}
		in.Rendered = renderAuthor(item, audiobooks)
	case *catalogv1alpha1.Album:
		if item.Status.Metadata == nil || item.Status.Path == "" {
			return nil, nil
		}
		var artist catalogv1alpha1.Artist
		if err := s.o.Reader.Get(ctx, types.NamespacedName{Namespace: item.Namespace, Name: item.Spec.ArtistRef}, &artist); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			return nil, nil
		}
		rootRef = artist.Spec.RootFolderRef
		in.Folder, in.Prev = item.Status.Path, item.Status.NFO
		in.Rendered = renderAlbum(item, &artist)
	case *catalogv1alpha1.Audiobook:
		if item.Status.Metadata == nil || item.Status.Path == "" {
			return nil, nil
		}
		rootRef = item.Spec.RootFolderRef
		in.Folder, in.Prev = item.Status.Path, item.Status.NFO
		var authorName string
		if ref := ptr.Deref(item.Spec.AuthorRef, ""); ref != "" {
			var author catalogv1alpha1.Author
			if err := s.o.Reader.Get(ctx, types.NamespacedName{Namespace: item.Namespace, Name: ref}, &author); err == nil && author.Status.Metadata != nil {
				authorName = author.Status.Metadata.Name
			} else if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
		}
		parts, err := s.parts(ctx, item)
		if err != nil {
			return nil, err
		}
		in.Rendered = renderAudiobook(item, authorName, parts)
	default:
		return nil, nil
	}
	var root catalogv1alpha1.RootFolder
	if err := s.o.Reader.Get(ctx, types.NamespacedName{Namespace: v.Item.GetNamespace(), Name: rootRef}, &root); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return nil, err
		}
		return nil, nil
	}
	in.Enabled = Enabled(&root)
	in.Mode = fileMode(&root)
	return in, nil
}

// Enabled is whether root's artists and albums get NFO files: a music or
// audiobook root whose naming.writeNFO is not false (2026-11-10 design §4.8).
func Enabled(root *catalogv1alpha1.RootFolder) bool {
	switch root.Spec.Kind {
	case catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootFolderKindAudiobook:
		return ptr.Deref(root.Spec.Naming.WriteNFO, true)
	}
	return false
}

// fileMode is the root's perms.fileMode as a mode, 0 (the agent's default)
// when unset or unparseable.
func fileMode(root *catalogv1alpha1.RootFolder) uint32 {
	m := root.Spec.Permissions.FileMode
	if m == "" {
		return 0
	}
	v, err := strconv.ParseUint(m, 8, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}

// renderArtist is artist.nfo and the artist image for a, its albums
// listed as Kodi's scraped discography is.
func renderArtist(a *catalogv1alpha1.Artist, albums []catalogv1alpha1.Album) *Rendered {
	poster := entryOf(a.Status.Artwork, catalogv1alpha1.ImageTypePoster)
	var thumb string
	if poster != nil {
		thumb = poster.SourceURL
	}
	r := &Rendered{Files: []schema.NFOFile{{Name: nfo.FileArtist, Content: nfo.Artist(a, albums, thumb)}}}
	if poster != nil {
		r.Images = append(r.Images, ImageFor(nfo.FileArtistImage, events.ArtworkKey(commonv1.MediaKindArtist, a.UID, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOriginal), poster.Digest))
	}
	r.Hash = HashOf(r.Files, r.Images)
	return r
}

// renderAuthor is artist.nfo and the artist image for au on an audiobook
// root: the author is the library's artist, each Audiobook an album.
func renderAuthor(au *catalogv1alpha1.Author, audiobooks []catalogv1alpha1.Audiobook) *Rendered {
	poster := entryOf(au.Status.Artwork, catalogv1alpha1.ImageTypePoster)
	var thumb string
	if poster != nil {
		thumb = poster.SourceURL
	}
	r := &Rendered{Files: []schema.NFOFile{{Name: nfo.FileArtist, Content: nfo.AuthorAsArtist(au, audiobooks, thumb)}}}
	if poster != nil {
		r.Images = append(r.Images, ImageFor(nfo.FileArtistImage, events.ArtworkKey(commonv1.MediaKindAuthor, au.UID, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOriginal), poster.Digest))
	}
	r.Hash = HashOf(r.Files, r.Images)
	return r
}

// orderedAlbums is albums in a stable order -- by release date, then title,
// then name -- so the rendered document, and its hash, do not move with
// the cache's listing order.
func orderedAlbums(albums []catalogv1alpha1.Album) []catalogv1alpha1.Album {
	out := slices.Clone(albums)
	slices.SortStableFunc(out, func(a, b catalogv1alpha1.Album) int {
		if c := cmp.Compare(releaseUnix(a.Status.Metadata), releaseUnix(b.Status.Metadata)); c != 0 {
			return c
		}
		if c := cmp.Compare(albumTitle(a), albumTitle(b)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

func albumTitle(a catalogv1alpha1.Album) string {
	if a.Status.Metadata == nil {
		return ""
	}
	return a.Status.Metadata.Title
}

func releaseUnix(md *catalogv1alpha1.AlbumMetadata) int64 {
	if md == nil || md.ReleaseDate == nil {
		return 0
	}
	return md.ReleaseDate.Unix()
}

// audiobooksOf lists au's Audiobooks (spec.authorRef) in a stable order --
// by release date, then title, then name -- filtering a namespaced List in
// Go: Audiobooks are few, and the audiobook package's own index on the
// field would conflict with a second registration.
func (s *Stage) audiobooksOf(ctx context.Context, au *catalogv1alpha1.Author) ([]catalogv1alpha1.Audiobook, error) {
	var list catalogv1alpha1.AudiobookList
	if err := s.o.Reader.List(ctx, &list, client.InNamespace(au.Namespace)); err != nil {
		return nil, err
	}
	var out []catalogv1alpha1.Audiobook
	for i := range list.Items {
		if ptr.Deref(list.Items[i].Spec.AuthorRef, "") == au.Name {
			out = append(out, list.Items[i])
		}
	}
	slices.SortStableFunc(out, func(a, b catalogv1alpha1.Audiobook) int {
		var ar, br int64
		var at, bt string
		if a.Status.Metadata != nil {
			at = a.Status.Metadata.Title
			if a.Status.Metadata.ReleaseDate != nil {
				ar = a.Status.Metadata.ReleaseDate.Unix()
			}
		}
		if b.Status.Metadata != nil {
			bt = b.Status.Metadata.Title
			if b.Status.Metadata.ReleaseDate != nil {
				br = b.Status.Metadata.ReleaseDate.Unix()
			}
		}
		if c := cmp.Compare(ar, br); c != 0 {
			return c
		}
		if c := cmp.Compare(at, bt); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out, nil
}

// renderAlbum is album.nfo and the cover for al by artist.
func renderAlbum(al *catalogv1alpha1.Album, artist *catalogv1alpha1.Artist) *Rendered {
	poster := entryOf(al.Status.Artwork, catalogv1alpha1.ImageTypePoster)
	var thumb string
	if poster != nil {
		thumb = poster.SourceURL
	}
	r := &Rendered{Files: []schema.NFOFile{{Name: nfo.FileAlbum, Content: nfo.Album(al, artist, thumb)}}}
	if poster != nil {
		r.Images = append(r.Images, ImageFor(nfo.FileAlbumImage, events.ArtworkKey(commonv1.MediaKindAlbum, al.UID, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOriginal), poster.Digest))
	}
	r.Hash = HashOf(r.Files, r.Images)
	return r
}

// renderAudiobook is album.nfo and the cover for ab, its author as the
// album artist and its parts as tracks.
func renderAudiobook(ab *catalogv1alpha1.Audiobook, authorName string, parts []nfo.Part) *Rendered {
	poster := entryOf(ab.Status.Artwork, catalogv1alpha1.ImageTypePoster)
	var thumb string
	if poster != nil {
		thumb = poster.SourceURL
	}
	r := &Rendered{Files: []schema.NFOFile{{Name: nfo.FileAlbum, Content: nfo.AudiobookAsAlbum(ab, authorName, parts, thumb)}}}
	if poster != nil {
		r.Images = append(r.Images, ImageFor(nfo.FileAlbumImage, events.ArtworkKey(commonv1.MediaKindAudiobook, ab.UID, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOriginal), poster.Digest))
	}
	r.Hash = HashOf(r.Files, r.Images)
	return r
}

// parts reads an audiobook's parts in play order: each MediaFile's probed
// runtime, titled by its chapter when the parts and chapters align one to
// one, else "Part N" (pkg/nfo).
func (s *Stage) parts(ctx context.Context, ab *catalogv1alpha1.Audiobook) ([]nfo.Part, error) {
	parts := make([]nfo.Part, 0, len(ab.Status.FileRefs))
	chapters := ab.Status.Metadata.Chapters
	aligned := len(chapters) > 0 && len(chapters) == len(ab.Status.FileRefs)
	for i, name := range ab.Status.FileRefs {
		p := nfo.Part{Index: int32(i + 1)}
		if aligned {
			p.Title = chapters[i].Title
		}
		var mf catalogv1alpha1.MediaFile
		if err := s.o.Reader.Get(ctx, types.NamespacedName{Namespace: ab.Namespace, Name: name}, &mf); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
		} else if mf.Status.MediaInfo != nil {
			p.DurationMs = mf.Status.MediaInfo.RuntimeMillis
		}
		parts = append(parts, p)
	}
	return parts, nil
}

func entryOf(entries []catalogv1alpha1.ArtworkEntry, t catalogv1alpha1.ImageType) *catalogv1alpha1.ArtworkEntry {
	for i := range entries {
		if entries[i].Type == t {
			return &entries[i]
		}
	}
	return nil
}

// Sources implements remediation.Watcher: an answer in clustarr-nfo wakes
// its item, of any of the four kinds.
func (s *Stage) Sources(ctrl.Manager) ([]remediation.Source, error) {
	if s.o.Bus == nil {
		return nil, nil
	}
	src := recordsource.NewItems(s.o.Bus, events.BucketNFO, func(item schema.ItemRef, _ []byte) []remediation.Key {
		k := remediation.KeyKind(item.Kind)
		if !applies(k) || item.Name == "" {
			return nil
		}
		return []remediation.Key{{Kind: k, Namespace: item.Namespace, Name: item.Name}}
	})
	return []remediation.Source{{Name: "S40/" + events.BucketNFO, Raw: src}}, nil
}
