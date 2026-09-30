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

// Package openlibrary is an in-house metadata.BookProvider for Open
// Library (https://openlibrary.org), the free, CC0, no-key book metadata
// source Readarr's own upstream (Goodreads) left behind
// (docs/research/metadata.md §2.4). There is no adopted Go client module,
// so every request goes through net/http directly.
package openlibrary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const defaultBaseURL = "https://openlibrary.org"

// editionsLimit is how many editions Book asks /works/{id}/editions.json
// for: one page, sized to BookMetadata.Editions' own
// +kubebuilder:validation:MaxItems=100. A popular work has thousands of
// editions (Pride and Prejudice: over four thousand) and nothing downstream
// can hold more than this many, so paging further would be spend with no
// consumer.
const editionsLimit = 100

// booksLimit is how many works Books asks the search for, the most
// edition-rich first. Open Library holds several works for one book, so
// about half collapse into another (bookKey): 100 works gave Dostoevsky
// about fifty books.
const booksLimit = 100

// defaultWikidataURL is where Author reads an author's name in the
// client's language (WithLanguage).
const defaultWikidataURL = "https://www.wikidata.org"

// Client is a metadata.BookProvider backed by Open Library.
type Client struct {
	http        *http.Client
	baseURL     string
	userAgent   string
	limiter     *rate.Limiter
	language    string // primary BCP-47 subtag, "" for none (WithLanguage)
	wikidataURL string
}

// WithLanguage makes the client prefer names and titles in language (a
// BCP-47 or ISO 639 code; MetadataProvider.spec.language, default "en"),
// the way TMDB is asked for language=en: Open Library catalogues a work
// under whatever title it was entered with -- Dostoevsky's novels are
// "Преступление и наказание" and "Братья Карамазовы" -- and an author under
// any spelling ("Fiódor Dostoievski"). Books titles each work by its
// edition in that language (the search API's lang), Book by its most
// common edition title in it, and Author takes Wikidata's label in it. An
// empty or unresolvable language keeps the catalogued titles.
func (c *Client) WithLanguage(language string) *Client {
	c.language = ""
	if t, ok := lang.Normalize(language); ok {
		c.language = primaryTag(string(t))
	}
	return c
}

// WithWikidataBaseURL overrides Wikidata's host, for tests.
func (c *Client) WithWikidataBaseURL(u string) *Client {
	c.wikidataURL = u
	return c
}

// primaryTag is l's primary language subtag, lower-cased: "pt-BR" -> "pt".
func primaryTag(l string) string {
	return strings.ToLower(strings.SplitN(l, "-", 2)[0])
}

// inLanguage reports whether the Open Library language code code (ISO
// 639-2, "eng") is the client's language.
func (c *Client) inLanguage(code string) bool {
	t, ok := lang.Normalize(strings.TrimPrefix(code, "/languages/"))
	return ok && c.language != "" && primaryTag(string(t)) == c.language
}

// New builds a Client. userAgent must identify the application and a
// contact -- Open Library rate-limits an unidentified client to 1rps
// instead of 3rps (docs/research/metadata.md §2.4). httpClient may be nil,
// in which case http.DefaultClient is used. baseURL overrides Open
// Library's default host -- tests pass an httptest.Server URL; production
// callers pass "".
func New(userAgent string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client {
	hc := httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{http: hc, baseURL: baseURL, userAgent: userAgent, limiter: limiter, wikidataURL: defaultWikidataURL}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "openlibrary" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{
		LookupBy: []string{metadata.KeyISBN13, metadata.KeyOpenLibraryWork, metadata.KeyOpenLibraryEdition},
		Search:   true,
	}
}

// SearchBooks searches Open Library's general work/edition index.
func (c *Client) SearchBooks(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.SearchBooks")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw struct {
		Docs []struct {
			Key              string `json:"key"`
			Title            string `json:"title"`
			FirstPublishYear int32  `json:"first_publish_year"`
			CoverID          int64  `json:"cover_i"`
		} `json:"docs"`
	}
	path := "/search.json?q=" + url.QueryEscape(q) + "&fields=key,title,first_publish_year,cover_i"
	if err := c.doGet(ctx, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: search failed", "query", q, "error", err)
		return nil, err
	}

	hits := make([]metadata.SearchHit, 0, len(raw.Docs))
	for _, d := range raw.Docs {
		hit := metadata.SearchHit{
			IDs:   metadata.ExternalIDs{metadata.KeyOpenLibraryWork: strings.TrimPrefix(d.Key, "/works/")},
			Title: d.Title,
			Year:  d.FirstPublishYear,
		}
		if d.CoverID != 0 {
			hit.Poster = coverURLByID(d.CoverID)
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// Author fetches a single author by their Open Library id
// (ids[metadata.KeyOpenLibraryAuthor]).
func (c *Client) Author(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Author, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Author")
	defer span.End()
	logger := logging.FromContext(ctx)

	olid, ok := ids[metadata.KeyOpenLibraryAuthor]
	if !ok {
		err := fmt.Errorf("openlibrary: Author requires %q in ExternalIDs", metadata.KeyOpenLibraryAuthor)
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw struct {
		Key       string          `json:"key"`
		Name      string          `json:"name"`
		Bio       json.RawMessage `json:"bio"`
		BirthDate string          `json:"birth_date"`
		DeathDate string          `json:"death_date"`
		RemoteIDs struct {
			Wikidata string `json:"wikidata"`
		} `json:"remote_ids"`
	}
	if err := c.doGet(ctx, "/authors/"+olid+".json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: author fetch failed", "olid", olid, "error", err)
		return nil, err
	}

	a := &metadata.Author{
		IDs:      metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: olid},
		Name:     raw.Name,
		Overview: decodeOpenLibraryText(raw.Bio),
	}
	if t, ok := parseLenientDate(raw.BirthDate); ok {
		a.Born = &t
	}
	if t, ok := parseLenientDate(raw.DeathDate); ok {
		a.Died = &t
	}
	if c.language != "" && wikidataQID.MatchString(raw.RemoteIDs.Wikidata) {
		name, err := c.wikidataLabel(ctx, raw.RemoteIDs.Wikidata)
		switch {
		case err != nil:
			// The Open Library name stands; a label is a nicety, never a
			// reason to fail the author.
			logger.DebugContext(ctx, "openlibrary: wikidata label unavailable", "olid", olid,
				"wikidata", raw.RemoteIDs.Wikidata, "error", err)
		case name != "":
			a.Name = name
		}
	}
	return a, nil
}

// wikidataQID is a Wikidata item id.
var wikidataQID = regexp.MustCompile(`^Q[0-9]+$`)

// wikidataLabel is Wikidata item qid's label in the client's language, ""
// when it has none.
func (c *Client) wikidataLabel(ctx context.Context, qid string) (string, error) {
	q := url.Values{
		"action": {"wbgetentities"}, "ids": {qid}, "props": {"labels"},
		"languages": {c.language}, "format": {"json"},
	}
	var raw struct {
		Entities map[string]struct {
			Labels map[string]struct {
				Value string `json:"value"`
			} `json:"labels"`
		} `json:"entities"`
	}
	if err := c.doGetURL(ctx, c.wikidataURL+"/w/api.php?"+q.Encode(), &raw); err != nil {
		return "", err
	}
	return strings.TrimSpace(raw.Entities[qid].Labels[c.language].Value), nil
}

// workRecord is Open Library's work record, as returned by
// /works/{OLID}.json and as each entry of /authors/{OLID}/works.json (the
// latter is a list of the same records -- verified against the live API:
// both carry key, title, authors, description, subjects, covers and, on
// some works, first_publish_date).
type workRecord struct {
	Key              string          `json:"key"`
	Title            string          `json:"title"`
	Description      json.RawMessage `json:"description"`
	Subjects         []string        `json:"subjects"`
	FirstPublishDate string          `json:"first_publish_date"`
	// Authors is [{"author": {"key": "/authors/OL..."}, "type": {...}}].
	// Each author is kept raw and decoded leniently by authorIDs: one
	// oddly-shaped legacy record must not fail a whole works listing.
	Authors []struct {
		Author json.RawMessage `json:"author"`
	} `json:"authors"`
}

// authorIDs returns the Open Library author ids a work credits, in order.
func (w workRecord) authorIDs() []string {
	var ids []string
	for _, a := range w.Authors {
		var ref struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(a.Author, &ref); err != nil || ref.Key == "" {
			continue
		}
		ids = append(ids, strings.TrimPrefix(ref.Key, "/authors/"))
	}
	return ids
}

// mapWork converts a work record into the normalized Book, without
// editions. fallbackAuthor is credited when the record names no author of
// its own (an entry of an author's works listing is that author's by
// definition).
func mapWork(w workRecord, fallbackAuthor string) metadata.Book {
	b := metadata.Book{
		IDs:       metadata.ExternalIDs{metadata.KeyOpenLibraryWork: strings.TrimPrefix(w.Key, "/works/")},
		AuthorIDs: w.authorIDs(),
		Title:     w.Title,
		Overview:  decodeOpenLibraryText(w.Description),
		Subjects:  w.Subjects,
	}
	if len(b.AuthorIDs) == 0 && fallbackAuthor != "" {
		b.AuthorIDs = []string{fallbackAuthor}
	}
	if t, ok := parseLenientDate(w.FirstPublishDate); ok {
		b.FirstPublished = &t
	}
	return b
}

// Books lists the works credited to authorID (an Open Library author id)
// through Open Library's search -- one request, the booksLimit most
// edition-rich works first -- with each work's title, subjects, authors,
// first-publication year and the languages it has editions in (Languages,
// which a metadata profile's allowedLanguages reads). With a language
// (WithLanguage) each work is titled by its edition in that language, the
// one the search's lang picks. Editions are not listed per work here (that
// is one more request per work); Book fetches them.
//
// It replaced /authors/{OLID}/works.json, which lists works in no useful
// order and under their catalogued titles only: an English reader adding
// Dostoevsky got 49 works in eleven languages and scripts (2026-09-30).
func (c *Client) Books(ctx context.Context, authorID string) ([]metadata.Book, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Books")
	defer span.End()
	logger := logging.FromContext(ctx)

	q := url.Values{
		"q":      {"author_key:" + authorID},
		"sort":   {"editions"},
		"limit":  {strconv.Itoa(booksLimit)},
		"fields": {"key,title,first_publish_year,subject,author_key,author_name,language,editions,editions.key,editions.title,editions.language"},
	}
	if c.language != "" {
		q.Set("lang", c.language)
	}
	var raw struct {
		Docs []searchWork `json:"docs"`
	}
	if err := c.doGet(ctx, "/search.json?"+q.Encode(), &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: author works search failed", "olid", authorID, "error", err)
		return nil, err
	}

	// One Book per book: Open Library holds many works for one -- The
	// Idiot four times, The Brothers Karamazov five (2026-09-30) -- and
	// the search's edition-count order puts the one to keep first.
	//
	// A work is a duplicate when either its chosen or its catalogued title
	// names a book already listed: an edition mistagged English ("Der
	// Idiot. Roman" on a work catalogued as "The idiot") is still another
	// Idiot. And with a language, a work with no title a reader of it can
	// use -- only Cyrillic, "Братья Карамазовы 1/2" -- is a translation or a
	// volume of a book listed already, so it is not listed.
	books := make([]metadata.Book, 0, len(raw.Docs))
	seen := make(map[string]bool, 2*len(raw.Docs))
	for _, w := range raw.Docs {
		b := c.mapSearchWork(w, authorID)
		if c.language != "" && !c.readable(b.Title) {
			continue
		}
		keys := []string{bookKey(b.Title, w.AuthorName), bookKey(w.Title, w.AuthorName)}
		if keys[0] == "" {
			keys[0] = w.Key // a title with nothing to compare never merges
		}
		if seen[keys[0]] || (keys[1] != "" && seen[keys[1]]) {
			continue
		}
		for _, k := range keys {
			if k != "" {
				seen[k] = true
			}
		}
		books = append(books, b)
	}
	return books, nil
}

// latinScript are the languages written in the Latin alphabet, as primary
// BCP-47 subtags: a title in one of them has a Latin letter.
var latinScript = map[string]bool{
	"af": true, "ca": true, "cs": true, "cy": true, "da": true, "de": true, "en": true, "es": true,
	"et": true, "eu": true, "fi": true, "fr": true, "ga": true, "gl": true, "hr": true, "hu": true,
	"id": true, "is": true, "it": true, "lt": true, "lv": true, "ms": true, "nb": true, "nl": true,
	"nn": true, "no": true, "pl": true, "pt": true, "ro": true, "sk": true, "sl": true, "sq": true,
	"sv": true, "sw": true, "tl": true, "tr": true, "vi": true,
}

// readable reports whether title can be in the client's language: for a
// Latin-script language, it has a Latin letter. Other scripts are not
// judged.
func (c *Client) readable(title string) bool {
	if !latinScript[c.language] {
		return true
	}
	for _, r := range title {
		if unicode.Is(unicode.Latin, r) {
			return true
		}
	}
	return false
}

// bookTitleNoise is what bookKey drops from a title before comparing:
// bracketed notes ("(Signet Classics)", "[Illustrated]"), and everything
// from a subtitle's or a translator note's ":" or ";" on.
var (
	bookBracketed = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)
	bookSubtitle  = regexp.MustCompile(`(\s/\s|[:;]).*$`)
	bookNonWord   = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	bookVolume    = regexp.MustCompile(`\s(volume|vol|part|book)\s+([0-9]+|[ivx]+)$`)
)

// bookEditionWords are marketing words an edition's title carries that do
// not make it another book: "Double Annotated", "Idiot Illustrated".
var bookEditionWords = map[string]bool{
	"annotated": true, "illustrated": true, "unabridged": true, "abridged": true, "edition": true,
}

// bookKey is what makes two works the same book: the title lower-cased,
// without brackets, subtitle (after ":", ";" or " / "), punctuation,
// articles, a trailing byline ("by <author>"), a trailing
// "Volume 1" or edition words. "The Idiot", "Idiot", "the idiot" and "The
// idiot (The Modern library of the world's best books)" are all "idiot";
// "Brothers Karamazov by Fyodor Dostoevsky" and "The Brothers Karamazov
// Volume 1 [EasyRead Large Edition]" are "brothers karamazov".
func bookKey(title string, authors []string) string {
	t := strings.ToLower(title)
	t = bookBracketed.ReplaceAllString(t, " ")
	t = bookSubtitle.ReplaceAllString(t, "")
	t = strings.Join(strings.Fields(bookNonWord.ReplaceAllString(t, " ")), " ")
	t = bookVolume.ReplaceAllString(t, "")
	words := strings.Fields(t)
	// A trailing "by ..." is a byline when it names the author, or when
	// two or more title words precede it: the record may spell the author
	// in another script ("Brothers Karamazov by Fyodor Dostoevsky" on a
	// work crediting "Фёдор Михайлович Достоевский"), while a title that
	// is itself "X by Y" is short -- "Stand by Me".
	if i := slices.Index(words, "by"); i >= 2 || (i > 0 && namesAnAuthor(words[i+1:], authors)) {
		words = words[:i]
	}
	kept := words[:0]
	for _, w := range words {
		if len(words) > 1 && (w == "the" || w == "a" || w == "an") {
			continue
		}
		kept = append(kept, w)
	}
	for len(kept) > 1 && bookEditionWords[kept[len(kept)-1]] {
		kept = kept[:len(kept)-1]
	}
	return strings.Join(kept, " ")
}

// namesAnAuthor reports whether words (lower-case, after a title's "by")
// name one of authors: a word shares its first four letters with a word of
// an author's name, since transliterations differ past that -- a title's
// "by Fyodor Dostoevsky" names Open Library's "Fiódor Dostoievski". "Stand
// by Me" names nobody.
func namesAnAuthor(words, authors []string) bool {
	prefix := func(w string) string {
		r := []rune(w)
		if len(r) < 4 {
			return ""
		}
		return string(r[:4])
	}
	for _, a := range authors {
		for _, n := range strings.Fields(strings.ToLower(bookNonWord.ReplaceAllString(a, " "))) {
			p := prefix(n)
			if p == "" {
				continue
			}
			for _, w := range words {
				if prefix(w) == p {
					return true
				}
			}
		}
	}
	return false
}

// searchWork is one work of Open Library's /search.json, with the fields
// Books asks for (verified against the live API).
type searchWork struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	FirstPublishYear int      `json:"first_publish_year"`
	Subject          []string `json:"subject"`
	AuthorKey        []string `json:"author_key"`
	AuthorName       []string `json:"author_name"`
	Language         []string `json:"language"` // ISO 639-2: "eng"
	Editions         struct {
		Docs []struct {
			Title    string   `json:"title"`
			Language []string `json:"language"`
		} `json:"docs"`
	} `json:"editions"`
}

// mapSearchWork converts a search hit into the normalized Book. The title
// is the work's edition in the client's language when the search found
// one, else the work's own; fallbackAuthor is credited when the hit names
// no author.
func (c *Client) mapSearchWork(w searchWork, fallbackAuthor string) metadata.Book {
	b := metadata.Book{
		IDs:       metadata.ExternalIDs{metadata.KeyOpenLibraryWork: strings.TrimPrefix(w.Key, "/works/")},
		AuthorIDs: w.AuthorKey,
		Title:     w.Title,
		Subjects:  w.Subject,
	}
	if len(b.AuthorIDs) == 0 && fallbackAuthor != "" {
		b.AuthorIDs = []string{fallbackAuthor}
	}
	if w.FirstPublishYear > 0 {
		t := time.Date(w.FirstPublishYear, 1, 1, 0, 0, 0, 0, time.UTC)
		b.FirstPublished = &t
	}
	for _, code := range w.Language {
		if t, ok := lang.Normalize(code); ok && !slices.Contains(b.Languages, string(t)) {
			b.Languages = append(b.Languages, string(t))
		}
	}
	for _, ed := range w.Editions.Docs {
		title := strings.TrimSpace(ed.Title)
		if title == "" || !slices.ContainsFunc(ed.Language, c.inLanguage) || !c.readable(title) {
			continue
		}
		if !strings.EqualFold(title, strings.TrimSpace(w.Title)) {
			b.Title = title
		}
		break
	}
	return b
}

// Book fetches a single work (ids[metadata.KeyOpenLibraryWork]) and one page
// of its editions (/works/{OLID}/editions.json, editionsLimit of them).
//
// FirstPublished is the earliest date among the work's own
// first_publish_date and every fetched edition's publish_date. Open
// Library's work-level date is sparse and sometimes later than an edition
// it lists, and "first published" means the earliest known publication, so
// taking the minimum is the reading that never reports a date later than
// one the provider itself shows.
//
// A failed editions fetch fails the call rather than returning the work
// with no editions: an empty Editions would read downstream as "this work
// has no editions" -- the metadata profile's SkipMissingISBN would act on
// it -- when the truth is that the fetch did not complete.
func (c *Client) Book(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Book, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Book")
	defer span.End()
	logger := logging.FromContext(ctx)

	workID, ok := ids[metadata.KeyOpenLibraryWork]
	if !ok {
		err := fmt.Errorf("openlibrary: Book requires %q in ExternalIDs", metadata.KeyOpenLibraryWork)
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw workRecord
	if err := c.doGet(ctx, "/works/"+workID+".json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: work fetch failed", "olid", workID, "error", err)
		return nil, err
	}
	b := mapWork(raw, "")
	b.IDs = metadata.ExternalIDs{metadata.KeyOpenLibraryWork: workID}

	var eds struct {
		Entries []editionResponse `json:"entries"`
	}
	path := "/works/" + workID + "/editions.json?limit=" + strconv.Itoa(editionsLimit)
	if err := c.doGet(ctx, path, &eds); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: work editions fetch failed", "olid", workID, "error", err)
		return nil, err
	}
	for _, e := range eds.Entries {
		ed := mapEdition(e)
		if ed.ReleaseDate != nil && (b.FirstPublished == nil || ed.ReleaseDate.Before(*b.FirstPublished)) {
			t := *ed.ReleaseDate
			b.FirstPublished = &t
		}
		b.Editions = append(b.Editions, ed)
	}

	if title := c.editionTitle(b.Title, eds.Entries); title != "" {
		b.Title = title
	}

	logger.DebugContext(ctx, "openlibrary: work fetched", "olid", workID, "title", b.Title, "editions", len(b.Editions))
	return &b, nil
}

// editionTitle is the title a work catalogued as workTitle takes in the
// client's language: its most common edition title in that language (the
// first seen on a tie), or "" to keep workTitle -- no language, no edition
// in it, or workTitle already one of those titles. Books picks its title
// the same way from the search's edition, so a refresh keeps it.
func (c *Client) editionTitle(workTitle string, editions []editionResponse) string {
	if c.language == "" {
		return ""
	}
	counts := map[string]int{}
	var order []string
	for _, e := range editions {
		title := strings.TrimSpace(e.Title)
		if title == "" || len(e.Languages) == 0 || !c.inLanguage(e.Languages[0].Key) || !c.readable(title) {
			continue
		}
		if strings.EqualFold(title, strings.TrimSpace(workTitle)) {
			return ""
		}
		if counts[title] == 0 {
			order = append(order, title)
		}
		counts[title]++
	}
	best := ""
	for _, t := range order {
		if counts[t] > counts[best] {
			best = t
		}
	}
	return best
}

// editionResponse is Open Library's edition record, as returned by
// /isbn/{isbn}.json, /books/{OLID}.json and each entry of
// /works/{OLID}/editions.json (field names verified against the live API).
type editionResponse struct {
	Key            string   `json:"key"`
	Title          string   `json:"title"`
	Subtitle       string   `json:"subtitle"`
	Publishers     []string `json:"publishers"`
	PublishDate    string   `json:"publish_date"`
	ISBN13         []string `json:"isbn_13"`
	Covers         []int64  `json:"covers"`
	NumberOfPages  int32    `json:"number_of_pages"`
	PhysicalFormat string   `json:"physical_format"`
	Languages      []struct {
		Key string `json:"key"` // "/languages/eng"
	} `json:"languages"`
	Identifiers struct {
		Amazon []string `json:"amazon"`
	} `json:"identifiers"`
}

// mapEdition converts an edition record into the normalized model. An id
// is only carried when it is well-formed (metadata.Validate): Open Library
// is user-edited, and a malformed ISBN or ASIN in the crosswalk is worse
// than none. Language is converted to BCP-47 at this boundary, as
// Edition.language's CRD field documents ("/languages/ger" -> "de"); a code
// pkg/lang cannot resolve is dropped rather than passed through in a
// vocabulary the field does not use.
func mapEdition(raw editionResponse) metadata.Edition {
	e := metadata.Edition{
		IDs:       metadata.ExternalIDs{metadata.KeyOpenLibraryEdition: strings.TrimPrefix(raw.Key, "/books/")},
		Title:     raw.Title,
		Subtitle:  raw.Subtitle,
		Format:    raw.PhysicalFormat,
		PageCount: raw.NumberOfPages,
	}
	if len(raw.ISBN13) > 0 && metadata.Validate(metadata.ExternalIDs{metadata.KeyISBN13: raw.ISBN13[0]}) == nil {
		e.IDs[metadata.KeyISBN13] = raw.ISBN13[0]
	}
	if len(raw.Identifiers.Amazon) > 0 && metadata.Validate(metadata.ExternalIDs{metadata.KeyASIN: raw.Identifiers.Amazon[0]}) == nil {
		e.IDs[metadata.KeyASIN] = raw.Identifiers.Amazon[0]
	}
	if len(raw.Publishers) > 0 {
		e.Publisher = raw.Publishers[0]
	}
	if len(raw.Languages) > 0 {
		if tag, ok := lang.Normalize(strings.TrimPrefix(raw.Languages[0].Key, "/languages/")); ok {
			e.Language = string(tag)
		}
	}
	if len(raw.Covers) > 0 && raw.Covers[0] > 0 {
		e.Images = []metadata.Image{{Type: metadata.ImageTypePoster, URL: coverURLByID(raw.Covers[0])}}
	}
	// publish_date is free text ("2003", "March 2003", "2003-01-01", ...),
	// not a fixed format -- a date that does not parse leaves ReleaseDate
	// nil rather than erroring the whole call.
	if t, ok := parseLenientDate(raw.PublishDate); ok {
		e.ReleaseDate = &t
	}
	return e
}

// Edition fetches a single edition by ISBN-13 (ids[metadata.KeyISBN13]).
func (c *Client) Edition(ctx context.Context, ids metadata.ExternalIDs) (*metadata.Edition, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.Edition")
	defer span.End()
	logger := logging.FromContext(ctx)

	isbn, ok := ids[metadata.KeyISBN13]
	if !ok {
		err := fmt.Errorf("openlibrary: Edition requires %q in ExternalIDs", metadata.KeyISBN13)
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw editionResponse
	if err := c.doGet(ctx, "/isbn/"+isbn+".json", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "openlibrary: edition fetch failed", "isbn", isbn, "error", err)
		return nil, err
	}

	e := mapEdition(raw)
	// The ISBN asked for is the one this edition is known by, whichever of
	// its isbn_13 values Open Library happens to list first.
	e.IDs[metadata.KeyISBN13] = isbn

	logger.DebugContext(ctx, "openlibrary: edition fetched", "isbn", isbn, "title", e.Title)
	return &e, nil
}

var _ metadata.BookProvider = (*Client)(nil)

// coverURLByID builds a large cover image URL from Open Library's numeric
// cover id (the "covers" array on an edition or "cover_i" on a search hit).
func coverURLByID(id int64) string {
	return fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", id)
}

// openLibraryText is a value that Open Library sometimes returns as a bare
// string and sometimes as {"type": "/type/text", "value": "..."} -- most
// visibly on Description/Bio fields. decodeOpenLibraryText handles both
// shapes and returns "" for anything else (absent, null, or a shape this
// client does not recognise) rather than erroring the whole call over a
// display-only field.
func decodeOpenLibraryText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var wrapped struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		return wrapped.Value
	}
	return ""
}

// parseLenientDate parses Open Library's free-text date fields
// (publish_date, birth_date, death_date), which mix "2003", "March 2003"
// and "2003-01-01" in the wild. It tries the formats this client has
// actually observed and reports ok=false rather than guessing further.
func parseLenientDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", "January 2, 2006", "January 2006", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	if year, err := strconv.Atoi(s); err == nil && year > 0 {
		return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), true
	}
	return time.Time{}, false
}

// doGet issues a GET request against path (relative to c.baseURL), waiting
// on the rate limiter and setting the contact User-Agent Open Library
// requires for its 3rps tier, and maps the HTTP status onto metadata's
// sentinel errors. A 200 body is read through metadata.DecodeJSON's cap;
// every other status is answered from the status alone, its body never
// read.
func (c *Client) doGet(ctx context.Context, path string, out any) error {
	return c.doGetURL(ctx, c.baseURL+path, out)
}

// doGetURL is doGet against an absolute URL (Wikidata's, for Author).
func (c *Client) doGetURL(ctx context.Context, rawURL string, out any) error {
	path := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		path = u.Path // errors name the path, never a query
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("openlibrary: build request: %w", err)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openlibrary: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := metadata.DecodeJSON(resp.Body, metadata.MaxResponseBytes, out); err != nil {
			return fmt.Errorf("openlibrary: %s: %w", path, err)
		}
		return nil
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "openlibrary", RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("openlibrary: unexpected status %d for %s", resp.StatusCode, path)
	}
}

// parseRetryAfter parses a Retry-After header value (seconds) into a
// duration, defaulting to zero when absent or malformed.
func parseRetryAfter(v string) time.Duration {
	seconds, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
