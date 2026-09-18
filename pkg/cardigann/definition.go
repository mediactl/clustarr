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

// Package cardigann implements the Cardigann v11 indexer-definition engine:
// YAML definitions validated against the bundled JSON schema, selectors over
// HTML/JSON/XML bodies, the 25 filter pipeline, login, search and download.
package cardigann

import (
	"fmt"

	yaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// Scalar decodes a YAML scalar of any primitive kind (string, int, float,
// bool, null) into its string form. The v11 schema allows this looseness in
// several places — settings defaults, SelectorBlock.case values, category
// mapping ids, login/search inputs — and the corpus relies on it: 1337x's
// `downloadvolumefactor: text: 0` is a bare YAML number, not the string "0".
type Scalar string

// UnmarshalYAML implements goccy/go-yaml's NodeUnmarshaler, decoding
// directly from the parsed AST node rather than BytesUnmarshaler's
// re-serialize-to-text-then-reparse path: verified against goccy/go-yaml
// v1.19.2, that text round trip mis-renders the raw control character
// U+000F that 1337x.yml's title filter chain contains as unparseable
// source, so every Scalar/ScalarList decode in this package goes through
// the node tree directly instead.
func (s *Scalar) UnmarshalYAML(node ast.Node) error {
	v, err := scalarNodeValue(node)
	if err != nil {
		return err
	}
	switch t := v.(type) {
	case nil:
		*s = ""
	case string:
		*s = Scalar(t)
	case bool, int, int64, uint64, float64:
		*s = Scalar(fmt.Sprint(t))
	default:
		return fmt.Errorf("cardigann: scalar: unsupported YAML type %T", v)
	}
	return nil
}

// scalarNodeValue extracts a scalar AST node's decoded Go value (string,
// int64, uint64, float64, bool or nil), erroring on a non-scalar node
// (mapping or sequence) where a scalar was expected.
func scalarNodeValue(node ast.Node) (any, error) {
	scalar, ok := node.(ast.ScalarNode)
	if !ok {
		return nil, fmt.Errorf("cardigann: scalar: unsupported node type %s", node.Type())
	}
	return scalar.GetValue(), nil
}

// ScalarList decodes a YAML value that is either a bare Scalar or a list of
// Scalars. FilterBlock.args and RowFilterBlock.args in the schema allow
// both forms — a filter with one argument is written `args: pattern`, one
// with several as `args: ["a", "b"]` (see re_replace throughout the corpus).
type ScalarList []string

// UnmarshalYAML implements goccy/go-yaml's NodeUnmarshaler; see Scalar's
// doc comment for why this decodes from the node tree, not raw bytes.
func (l *ScalarList) UnmarshalYAML(node ast.Node) error {
	if arr, ok := node.(ast.ArrayNode); ok {
		var out ScalarList
		it := arr.ArrayRange()
		for it.Next() {
			var s Scalar
			if err := s.UnmarshalYAML(it.Value()); err != nil {
				return fmt.Errorf("cardigann: args: %w", err)
			}
			out = append(out, string(s))
		}
		*l = out
		return nil
	}
	var one Scalar
	if err := one.UnmarshalYAML(node); err != nil {
		return fmt.Errorf("cardigann: args: %w", err)
	}
	*l = ScalarList{string(one)}
	return nil
}

// DefinitionType is the schema's own tracker-privacy enum, verbatim:
// "public" | "semi-private" | "private" — NOT the IndexerDefinitionStatus
// CRD's camelCase equivalent; the indexer controller maps between them.
type DefinitionType string

// FieldEntry is one declared search.fields entry, name plus its selector.
type FieldEntry struct {
	Name  string
	Block SelectorBlock
}

// OrderedFields preserves search.fields' file order: CardigannParser.ParseFields
// evaluates fields in declaration order and later fields (title) read earlier
// ones via .Result.<name> (see 1337x's title_optional/title_default/title chain).
type OrderedFields []FieldEntry

// UnmarshalYAML implements goccy/go-yaml's NodeUnmarshaler, decoding each
// map entry directly from its AST node rather than via yaml.MapSlice plus a
// re-Marshal/re-Unmarshal round trip through YAML text: verified against
// goccy/go-yaml v1.19.2, that text round trip mis-encodes the raw control
// character U+000F that 1337x.yml's title filter chain contains (it comes
// back unparseable), so this decodes straight from the node tree instead.
// ast.MapNode.MapRange() preserves search.fields' declaration order, which
// CardigannParser.ParseFields depends on: later fields (title) read earlier
// ones via .Result.<name> (see 1337x's title_optional/title_default/title
// chain).
func (f *OrderedFields) UnmarshalYAML(node ast.Node) error {
	mapNode, ok := node.(ast.MapNode)
	if !ok {
		return fmt.Errorf("cardigann: fields: expected a mapping, got %s", node.Type())
	}
	*f = nil
	it := mapNode.MapRange()
	for it.Next() {
		var name string
		if err := yaml.NodeToValue(it.Key(), &name); err != nil {
			return fmt.Errorf("cardigann: fields: key %s: %w", it.Key().String(), err)
		}
		var block SelectorBlock
		if err := yaml.NodeToValue(it.Value(), &block); err != nil {
			return fmt.Errorf("cardigann: field %q: %w", name, err)
		}
		*f = append(*f, FieldEntry{Name: name, Block: block})
	}
	return nil
}

// Definition is a decoded Cardigann v11 indexer definition.
type Definition struct {
	ID              string         `yaml:"id"`
	Name            string         `yaml:"name"`
	Description     string         `yaml:"description"`
	Language        string         `yaml:"language"`
	Type            DefinitionType `yaml:"type"`
	Replaces        []string       `yaml:"replaces"`
	Encoding        string         `yaml:"encoding"`
	FollowRedirect  bool           `yaml:"followredirect"`
	TestLinkTorrent *bool          `yaml:"testlinktorrent"`
	RequestDelay    float64        `yaml:"requestDelay"` // seconds

	Links        []string `yaml:"links"`
	LegacyLinks  []string `yaml:"legacylinks"`
	Certificates []string `yaml:"certificates"`

	Caps     Caps            `yaml:"caps"`
	Settings []SettingsField `yaml:"settings"`
	Login    *LoginBlock     `yaml:"login"`
	Search   SearchBlock     `yaml:"search"`
	Download *DownloadBlock  `yaml:"download"`
}

// SettingsField is one entry in settings: a piece of per-indexer
// configuration the definition author exposes (an API key, a sort order, a
// checkbox toggle, ...).
type SettingsField struct {
	Name     string            `yaml:"name"`
	Label    string            `yaml:"label"`
	Type     string            `yaml:"type"` // info|text|password|checkbox|select|info_category_8000|info_cookie|info_flaresolverr|info_useragent
	Default  Scalar            `yaml:"default"`
	Options  map[string]string `yaml:"options"`
	Defaults []string          `yaml:"defaults"`
}

// Caps describes what a Definition can search for: its category table and
// the query parameters each search mode accepts.
type Caps struct {
	// Categories is the legacy form: trackerID -> IndexerCategories name.
	// Mutually exclusive with CategoryMappings per the schema's oneOf.
	Categories       map[string]string `yaml:"categories"`
	CategoryMappings []CategoryMapping `yaml:"categorymappings"`
	// Modes maps "search"|"tv-search"|"movie-search"|"music-search"|"book-search"
	// to the query parameters that mode accepts.
	Modes             map[string][]string `yaml:"modes"`
	AllowRawSearch    bool                `yaml:"allowrawsearch"`
	AllowTVSearchIMDB bool                `yaml:"allowtvsearchimdb"`
}

// CategoryMapping is one tracker-category-id -> canonical-category entry.
type CategoryMapping struct {
	ID      Scalar `yaml:"id"`  // tracker's own category id, string or int in the YAML
	Cat     string `yaml:"cat"` // one of the 71 canonical IndexerCategories enum names
	Desc    string `yaml:"desc"`
	Default bool   `yaml:"default"`
}

// LoginBlock describes how Engine.Login authenticates against this
// indexer before Search/Download run.
type LoginBlock struct {
	Method  string   `yaml:"method"` // ""(=form)|form|post|cookie|get|oneurl
	Cookies []string `yaml:"cookies"`

	Path       string `yaml:"path"`
	SubmitPath string `yaml:"submitpath"`
	Form       string `yaml:"form"`

	Captcha *CaptchaBlock `yaml:"captcha"`

	Inputs    map[string]Scalar `yaml:"inputs"`
	Selectors bool              `yaml:"selectors"`

	SelectorInputs    map[string]SelectorBlock `yaml:"selectorinputs"`
	GetSelectorInputs map[string]SelectorBlock `yaml:"getselectorinputs"`

	Error []ErrorBlock   `yaml:"error"`
	Test  *PageTestBlock `yaml:"test"`

	Headers map[string][]string `yaml:"headers"`
}

// CaptchaBlock describes a captcha challenge Engine.Login detects but never
// solves.
type CaptchaBlock struct {
	Type     string `yaml:"type"` // "image"|"text"
	Selector string `yaml:"selector"`
	Input    string `yaml:"input"`
}

// ErrorBlock is one condition that, when matched on a login/search response,
// signals failure.
type ErrorBlock struct {
	Path     string         `yaml:"path"`
	Selector string         `yaml:"selector"`
	Message  *SelectorBlock `yaml:"message"`
}

// PageTestBlock verifies a login succeeded by checking Selector matches on
// the page at Path.
type PageTestBlock struct {
	Path     string `yaml:"path"`
	Selector string `yaml:"selector"`
}

// SelectorBlock is a single extraction rule: a selector plus the
// case/remove/default/text/filters pipeline SelectorBlock.Extract applies.
type SelectorBlock struct {
	Selector  string            `yaml:"selector"`
	Attribute string            `yaml:"attribute"`
	Optional  bool              `yaml:"optional"`
	Default   *Scalar           `yaml:"default"` // requires Optional per schema dependentRequired
	Case      map[string]Scalar `yaml:"case"`
	Remove    string            `yaml:"remove"` // a nested selector to strip before reading text
	Text      *Scalar           `yaml:"text"`   // literal or template, replaces Selector entirely
	Filters   []FilterBlock     `yaml:"filters"`
}

// SearchBlock describes how Engine.Search builds and fans out requests, and
// how it extracts rows and fields from each response.
type SearchBlock struct {
	Path             string              `yaml:"path"`
	Paths            []SearchPathBlock   `yaml:"paths"` // exactly one of Path/Paths is set (schema oneOf)
	AllowEmptyInputs bool                `yaml:"allowEmptyInputs"`
	Inputs           map[string]Scalar   `yaml:"inputs"`
	Headers          map[string][]string `yaml:"headers"`

	KeywordsFilters      []FilterBlock `yaml:"keywordsfilters"`
	PreprocessingFilters []FilterBlock `yaml:"preprocessingfilters"`

	Error []ErrorBlock `yaml:"error"`
	Rows  RowsBlock    `yaml:"rows"`

	Fields OrderedFields `yaml:"fields"`
}

// SearchPathBlock is one entry of search.paths: a request template plus the
// tracker categories it applies to.
type SearchPathBlock struct {
	Path           string `yaml:"path"`
	Method         string `yaml:"method"` // default GET
	FollowRedirect bool   `yaml:"followredirect"`
	// Categories are tracker category ids; a leading "!" excludes.
	// Empty/absent = path always matches.
	Categories     []Scalar          `yaml:"categories"`
	Inputs         map[string]Scalar `yaml:"inputs"`
	InheritInputs  bool              `yaml:"inheritinputs"`
	QuerySeparator string            `yaml:"queryseparator"`
	Response       *ResponseBlock    `yaml:"response"` // nil = HTML
}

// ResponseBlock declares a non-HTML response body shape.
type ResponseBlock struct {
	Type             string `yaml:"type"` // "json"|"xml"
	NoResultsMessage string `yaml:"noResultsMessage"`
}

// RowsBlock locates each result row within a response and how many rows to
// skip/expect.
type RowsBlock struct {
	SelectorBlock `yaml:",inline"`

	After                           int            `yaml:"after"` // KNOWN GAP: decoded, not implemented — see search.go
	Multiple                        bool           `yaml:"multiple"`
	MissingAttributeEqualsNoResults bool           `yaml:"missingAttributeEqualsNoResults"`
	DateHeaders                     *SelectorBlock `yaml:"dateheaders"` // KNOWN GAP: decoded, not implemented
	Count                           *SelectorBlock `yaml:"count"`
}

// FilterBlock is one named transform applied by SelectorBlock.Extract or a
// keywords/preprocessing pipeline.
type FilterBlock struct {
	Name string     `yaml:"name"` // one of the 25 names in filters.go's Filters map
	Args ScalarList `yaml:"args"`
}

// DownloadBlock describes how Engine.Download resolves a search result's
// link into downloadable content.
type DownloadBlock struct {
	Method    string              `yaml:"method"`
	Before    *BeforeBlock        `yaml:"before"`
	Selectors []SelectorField     `yaml:"selectors"`
	InfoHash  *InfoHashBlock      `yaml:"infohash"`
	Headers   map[string][]string `yaml:"headers"`
}

// BeforeBlock is an optional request Engine.Download issues before fetching
// the download link itself (e.g. to warm a session or acquire a token).
type BeforeBlock struct {
	Path           string            `yaml:"path"`
	PathSelector   *SelectorField    `yaml:"pathselector"`
	Method         string            `yaml:"method"`
	Inputs         map[string]Scalar `yaml:"inputs"`
	QuerySeparator string            `yaml:"queryseparator"`
}

// InfoHashBlock builds a magnet URI from an extracted hash and title when no
// DownloadBlock.Selectors entry matched.
type InfoHashBlock struct {
	Hash              SelectorField `yaml:"hash"`
	Title             SelectorField `yaml:"title"`
	UseBeforeResponse bool          `yaml:"usebeforeresponse"`
}

// SelectorField is DownloadBlock's smaller selector shape: no
// case/default/text, just a selector, attribute and filter chain.
type SelectorField struct {
	Selector          string        `yaml:"selector"`
	Attribute         string        `yaml:"attribute"`
	UseBeforeResponse bool          `yaml:"usebeforeresponse"`
	Filters           []FilterBlock `yaml:"filters"`
}

// Load validates data against the embedded v11 schema (see Validate) and
// decodes it into a Definition. It is the only public entry point that
// combines the two — the indexer controller calls it once per
// IndexerDefinition.spec.yaml and once per bundled definition at startup.
func Load(data []byte) (*Definition, error) {
	if err := Validate(data); err != nil {
		return nil, err
	}
	var def Definition
	if err := yaml.UnmarshalWithOptions(data, &def, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("cardigann: decode: %w", err)
	}
	return &def, nil
}
