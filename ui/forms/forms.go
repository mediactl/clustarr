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

// Package forms gives each Settings kind its form: an overlay ([Kind])
// that arranges the fields ui/schema finds in the kind's CRD into titled
// groups, hides workload plumbing, names the credentials behind each
// secretRef and the live objects a reference field chooses from; and
// [Build], which turns the overlay, the schema and an object's spec into
// the render tree ([Form]) ui/views draws (settings CRUD design,
// 2026-09-24). A field the overlay does not place still renders, under
// Other, so nothing the CRD accepts is unreachable.
package forms

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/schema"
)

// Kind is one settings kind's form definition over its [actions.ConfigKind].
type Kind struct {
	actions.ConfigKind
	// Title heads the kind's section ("Download clients"); Singular names
	// one ("download client").
	Title    string
	Singular string
	// Groups are the form's sections, in order; a path names a scalar, a
	// whole object (its children in schema order), an array or a map.
	Groups []Group
	// Hidden paths (and everything under them) never render.
	Hidden []string
	// Labels overrides the label derived from a field's name.
	Labels map[string]string
	// Secrets names the credentials behind each secretRef.
	Secrets []Secret
	// Refs names the live objects a string field chooses from.
	Refs map[string]Ref
	// ReadOnlyOnEdit are the immutable paths, editable only on a new form.
	ReadOnlyOnEdit []string
	// Only shows a field (its path and everything under it) only while its
	// condition holds: a field another field's choice makes meaningless (a
	// movie root's series type) is not drawn for that choice. An entry on
	// an object holds for every field under it, beside their own.
	Only map[string]*When
	// OptionsWhen offers one option of a field's choice only while its
	// condition holds: path, then the option's value.
	OptionsWhen map[string]map[string]*When
	// RefOptions offers a reference field's live object only while the
	// condition its class gives holds (a root folder is offered the quality
	// profiles of its own media kind): path, then a function of
	// Option.Class; nil offers it always.
	RefOptions map[string]func(class string) *When
	// Checks are the paths of arrays of an enumerated string drawn as one
	// checkbox per value, where the order of the values means nothing.
	Checks []string
	// External are paths the page draws itself (the Indexer's settings, in
	// the section its definition fills): placed, never emitted here.
	External []string
	// Ensure completes a decoded spec with what a form cannot express
	// (an empty object a CRD rule requires). Optional.
	Ensure func(spec map[string]any)
	// Choice is a selector the schema lacks (ImportList's provider is which
	// sub-object exists): rendered ahead of the groups, and applied to a
	// decoded spec before Ensure. Optional.
	Choice *Choice
	// Help is shown under the form's heading.
	Help string
}

// Group is one section of the form.
type Group struct {
	Title string
	Help  string
	Paths []string
	// When shows the section only while the field at When.Path has one
	// of When.Values ("" for unset).
	When *When
	// Advanced collapses the section by default.
	Advanced bool
}

// When is a condition on another field's value: it holds while the field at
// Path has one of Values ("" for unset), or with Not none of them. A field
// of several values (a checkbox group, a list) has each of them; a field
// that is itself hidden by a condition reads as unset. And is a further
// condition that must hold too.
type When struct {
	Path   string
	Values []string
	Not    bool
	And    *When
}

// Attr renders the condition as settings.js reads it: clauses joined by
// "&", each "path=v1|v2" or "path!=v1|v2".
func (w *When) Attr() string {
	if w == nil {
		return ""
	}
	op := "="
	if w.Not {
		op = "!="
	}
	out := w.Path + op + strings.Join(w.Values, "|")
	if w.And != nil {
		out += "&" + w.And.Attr()
	}
	return out
}

// Holds evaluates the condition against values, a reader of a field's
// current values; the server's twin of settings.js, used to shape a
// submission.
func (w *When) Holds(values func(path string) []string) bool {
	if w == nil {
		return true
	}
	cur := values(w.Path)
	if len(cur) == 0 {
		cur = []string{""}
	}
	hit := false
	for _, v := range cur {
		if slices.Contains(w.Values, v) {
			hit = true
			break
		}
	}
	if hit == w.Not {
		return false
	}
	return w.And.Holds(values)
}

// and joins two conditions; either may be nil.
func and(a, b *When) *When {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := *a
	out.And = and(a.And, b)
	return &out
}

// Secret describes the credentials behind a secretRef field: the Secret
// data keys the kind's consumer reads.
type Secret struct {
	// Path is the secretRef field's schema path ("secretRef",
	// "usenet.providers[].secretRef").
	Path string
	Keys []Key
	// Help replaces the Secret name input's help, for a kind whose entries
	// are not all listed here (an Indexer's are its definition's).
	Help string
}

// Key is one Secret data entry a form collects.
type Key struct {
	Key      string
	Label    string
	Help     string
	Required bool
	When     *When
}

// Ref names a source of choices: the slug of the kind whose objects fill
// a reference field's select.
type Ref string

const (
	RefQualityProfiles    Ref = "qualityprofiles"
	RefTranscodeProfiles  Ref = "transcodeprofiles"
	RefSubtitleProfiles   Ref = "subtitleprofiles"
	RefDelayProfiles      Ref = "delayprofiles"
	RefDownloadClients    Ref = "downloadclients"
	RefIndexerDefinitions Ref = "indexerdefinitions"
	RefIndexerProxies     Ref = "indexerproxies"
	RefRootFolders        Ref = "rootfolders"
)

// Choice is a selector the schema lacks: for a kind whose variant is
// which sub-object exists (an ImportList's provider), one control picks
// it. It renders as a required select named ControlName() in a section of
// its own ahead of the groups; sections and Secret keys depend on it
// through When{Path: ControlName()}; Current reads the variant off a
// stored spec (an edit, or a submission being re-rendered); Shape shapes a
// decoded spec to the chosen variant before the write, and leaves the
// spec alone for "".
type Choice struct {
	Name           string
	Label          string
	Help           string
	Options        []Option
	ReadOnlyOnEdit bool
	Current        func(spec map[string]any) string
	Shape          func(spec map[string]any, value string)
}

// ControlName is the form input a Choice posts as: "__choice.<Name>", a
// name no schema path can take, so Decode ignores it.
func (c Choice) ControlName() string { return "__choice." + c.Name }

// Choices are the options each Ref offers, listed by the page from live
// objects. A Ref with no entry renders as a text input.
type Choices map[Ref][]Option

// Option is one select choice.
type Option struct {
	Value string
	Label string
	// Class is what a live object is to a RefOptions rule: a quality
	// profile's media kind, a download client's protocol, a root folder's
	// kind.
	Class string
	// When offers the option only while it holds.
	When *When
}

// Mode is what the form is for.
type Mode string

const (
	ModeNew  Mode = "new"
	ModeEdit Mode = "edit"
)

// ControlType is how a control renders.
type ControlType string

const (
	ControlText     ControlType = "text"
	ControlNumber   ControlType = "number"
	ControlCheckbox ControlType = "checkbox"
	ControlSelect   ControlType = "select"
	ControlLines    ControlType = "lines"    // an array of scalars, one per line
	ControlMap      ControlType = "map"      // key/value pairs
	ControlRows     ControlType = "rows"     // an array of objects
	ControlPassword ControlType = "password" // a Secret entry, never read back
	ControlChecks   ControlType = "checks"   // an array of an enumerated string, a checkbox per value
	ControlHidden   ControlType = "hidden"   // a value the form carries and conditions read
	ControlNote     ControlType = "note"     // text only: Label and Help
)

// Form is the render tree of one kind's form.
type Form struct {
	Kind      Kind
	Mode      Mode
	Namespace string
	Name      string
	Sections  []Section
}

// Section is one rendered group.
type Section struct {
	Title    string
	Help     string
	When     *When
	Advanced bool
	Controls []Control
	// Dynamic has the page fetch the section's content again when a field
	// changes.
	Dynamic *Dynamic
}

// Dynamic is a section whose content depends on another field's value in a
// way no condition expresses (an Indexer's settings are its definition's):
// while the field named Watch changes, the page GETs URL with the form's
// Include fields and swaps the answer in for the section's content.
type Dynamic struct {
	URL     string
	Watch   string
	Include []string
}

// Control is one rendered input.
type Control struct {
	Type        ControlType
	Name        string
	Label       string
	Help        string
	Value       string
	Checked     bool
	Required    bool
	ReadOnly    bool
	Placeholder string
	Options     []Option
	Min, Max    *float64
	Step        string
	Lines       []string
	Pairs       []Pair
	Rows        []Row
	// Template is a row's controls with "__i__" for the index, for the
	// Add button.
	Template []Control
	When     *When
	// Checked values of a ControlChecks.
	Values []string
	// Wide spans the control across the section's columns.
	Wide bool
	// Fill, on a ControlHidden, names an input the page fills with Value
	// when it is empty or still holds what an earlier fill put there.
	Fill string
}

// Pair is one map entry.
type Pair struct {
	Key   string
	Value string
}

// Row is one element of an array of objects.
type Row struct {
	Index    int
	Controls []Control
}

// Kinds returns every kind's form definition, in the Settings page's order.
func Kinds() []Kind {
	out := make([]Kind, len(kinds))
	copy(out, kinds)
	return out
}

// Lookup finds a kind's form by its slug.
func Lookup(slug string) (Kind, bool) {
	for _, k := range kinds {
		if k.Slug == slug {
			return k, true
		}
	}
	return Kind{}, false
}

// builder carries what every emit needs.
type builder struct {
	kind    Kind
	spec    map[string]any
	choices Choices
	mode    Mode
	placed  []string
}

// Build renders kind's form for spec (nil for a new object).
func Build(kind Kind, root *schema.Field, spec map[string]any, choices Choices, mode Mode) *Form {
	b := &builder{kind: kind, spec: spec, choices: choices, mode: mode}
	b.placed = append(b.placed, kind.External...)
	f := &Form{Kind: kind, Mode: mode}
	if c := kind.Choice; c != nil {
		ctl := Control{Type: ControlSelect, Name: c.ControlName(), Label: c.Label, Help: c.Help, Required: true, Options: c.Options, ReadOnly: mode == ModeEdit && c.ReadOnlyOnEdit}
		if c.Current != nil {
			ctl.Value = c.Current(spec)
		}
		f.Sections = append(f.Sections, Section{Title: c.Label, Controls: []Control{ctl}})
	}
	for _, g := range kind.Groups {
		s := Section{Title: g.Title, Help: g.Help, When: g.When, Advanced: g.Advanced}
		for _, p := range g.Paths {
			field := root.Lookup(p)
			if field == nil {
				continue
			}
			b.placed = append(b.placed, p)
			s.Controls = append(s.Controls, b.emit(field, p)...)
		}
		if len(s.Controls) > 0 {
			f.Sections = append(f.Sections, s)
		}
	}
	other := Section{Title: "Other", Help: "Fields the form does not arrange; the CRD accepts them all."}
	other.Controls = b.rest(root)
	if len(other.Controls) > 0 {
		f.Sections = append(f.Sections, other)
	}
	return f
}

// rest emits every field no group placed and no rule hides.
func (b *builder) rest(f *schema.Field) []Control {
	var out []Control
	for _, c := range f.Fields {
		if b.hidden(c.Path) || b.isPlaced(c.Path) {
			continue
		}
		if c.Type == schema.TypeObject && b.partlyPlaced(c.Path) {
			out = append(out, b.rest(c)...)
			continue
		}
		out = append(out, b.emit(c, c.Path)...)
	}
	return out
}

func (b *builder) isPlaced(path string) bool {
	for _, p := range b.placed {
		if path == p || strings.HasPrefix(path, p+".") {
			return true
		}
	}
	return false
}

func (b *builder) partlyPlaced(path string) bool {
	for _, p := range b.placed {
		if strings.HasPrefix(p, path+".") {
			return true
		}
	}
	return false
}

func (b *builder) hidden(path string) bool {
	for _, h := range b.kind.Hidden {
		if path == h || strings.HasPrefix(path, h+".") {
			return true
		}
	}
	return false
}

// emit renders field f whose inputs are named from name (the schema path
// with indices filled in; f.Path keeps the "[]" form for the overlay's
// lookups).
func (b *builder) emit(f *schema.Field, name string) []Control {
	if b.hidden(f.Path) || b.external(f.Path) {
		return nil
	}
	out := b.emitField(f, name)
	if w, ok := b.kind.Only[f.Path]; ok {
		for i := range out {
			out[i].When = and(w, out[i].When)
		}
	}
	return out
}

func (b *builder) external(path string) bool {
	for _, e := range b.kind.External {
		if path == e || strings.HasPrefix(path, e+".") {
			return true
		}
	}
	return false
}

func (b *builder) emitField(f *schema.Field, name string) []Control {
	switch f.Type {
	case schema.TypeObject:
		if s := b.secretFor(f.Path); s != nil {
			return b.secret(f, name, s)
		}
		var out []Control
		for _, c := range f.Fields {
			out = append(out, b.emit(c, name+"."+c.Name)...)
		}
		return out
	case schema.TypeArray:
		if f.Items.Type == schema.TypeObject {
			return []Control{b.rows(f, name)}
		}
		if len(f.Items.Enum) > 0 && slices.Contains(b.kind.Checks, f.Path) {
			c := b.control(f, name, ControlChecks)
			for _, e := range f.Items.Enum {
				c.Options = append(c.Options, Option{Value: e, Label: e, When: b.kind.OptionsWhen[f.Path][e]})
			}
			for _, v := range asSlice(b.value(name)) {
				c.Values = append(c.Values, format(v))
			}
			return []Control{c}
		}
		c := b.control(f, name, ControlLines)
		for _, v := range asSlice(b.value(name)) {
			c.Lines = append(c.Lines, format(v))
		}
		return []Control{c}
	case schema.TypeMap:
		c := b.control(f, name, ControlMap)
		if m, ok := b.value(name).(map[string]any); ok {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				c.Pairs = append(c.Pairs, Pair{Key: k, Value: format(m[k])})
			}
		}
		return []Control{c}
	case schema.TypeBoolean:
		c := b.control(f, name, ControlCheckbox)
		v := b.value(name)
		if v == nil {
			v = f.Default
		}
		c.Checked, _ = v.(bool)
		return []Control{c}
	case schema.TypeInteger, schema.TypeNumber:
		c := b.control(f, name, ControlNumber)
		c.Min, c.Max = f.Minimum, f.Maximum
		if f.Type == schema.TypeInteger {
			c.Step = "1"
		} else {
			c.Step = "any"
		}
		c.Value = format(b.value(name))
		return []Control{c}
	default:
		c := b.control(f, name, ControlText)
		c.Value = format(b.value(name))
		if ref, ok := b.kind.Refs[f.Path]; ok {
			if opts := b.choices[ref]; len(opts) > 0 {
				c.Type = ControlSelect
				c.Options = []Option{{Value: "", Label: "—"}}
				rule := b.kind.RefOptions[f.Path]
				for _, o := range opts {
					if rule != nil {
						o.When = rule(o.Class)
					}
					c.Options = append(c.Options, o)
				}
			}
		} else if len(f.Enum) > 0 {
			c.Type = ControlSelect
			if !f.Required {
				c.Options = append(c.Options, Option{Value: "", Label: "—"})
			}
			for _, e := range f.Enum {
				c.Options = append(c.Options, Option{Value: e, Label: e})
			}
		}
		return []Control{c}
	}
}

func (b *builder) control(f *schema.Field, name string, t ControlType) Control {
	c := Control{Type: t, Name: name, Label: b.label(f), Help: firstSentence(f.Description), Required: f.Required}
	if f.Default != nil && t != ControlCheckbox {
		c.Placeholder = format(f.Default)
	}
	if b.mode == ModeEdit {
		for _, p := range b.kind.ReadOnlyOnEdit {
			if f.Path == p {
				c.ReadOnly = true
			}
		}
	}
	return c
}

func (b *builder) label(f *schema.Field) string {
	if l, ok := b.kind.Labels[f.Path]; ok {
		return l
	}
	return Humanize(f.Name)
}

func (b *builder) secretFor(path string) *Secret {
	for i := range b.kind.Secrets {
		if b.kind.Secrets[i].Path == path {
			return &b.kind.Secrets[i]
		}
	}
	return nil
}

// secret renders a secretRef: its name, then a password input per entry
// the consumer reads, named "__secret.<ref path>.<key>" so the handler can
// tell them from spec fields; a stored value is never read back.
func (b *builder) secret(f *schema.Field, name string, s *Secret) []Control {
	nameField := f.Lookup("name")
	// Never required: the handler names the Secret after the object when
	// the input is blank and any entry is given.
	keys := make([]string, 0, len(s.Keys))
	for _, k := range s.Keys {
		keys = append(keys, k.Key)
	}
	c := Control{
		Type: ControlText, Name: name + ".name", Label: b.label(f),
		Help: "The Secret holding the entries below, under the keys " + strings.Join(keys, ", ") + "; created or updated from them. Blank names it after this object. An existing Secret must use those key names.",
	}
	if s.Help != "" {
		c.Help = s.Help
	}
	if nameField != nil {
		c.Value = format(b.value(name + ".name"))
	}
	out := []Control{c}
	for _, k := range s.Keys {
		out = append(out, Control{Type: ControlPassword, Name: "__secret." + name + "." + k.Key, Label: k.Label, Help: k.Help, Required: k.Required, When: k.When})
	}
	return out
}

// rows renders an array of objects: a row per element and a template.
func (b *builder) rows(f *schema.Field, name string) Control {
	c := b.control(f, name, ControlRows)
	for i := range asSlice(b.value(name)) {
		row := Row{Index: i}
		for _, child := range f.Items.Fields {
			row.Controls = append(row.Controls, b.emit(child, fmt.Sprintf("%s.%d.%s", name, i, child.Name))...)
		}
		c.Rows = append(c.Rows, row)
	}
	saved := b.spec
	b.spec = nil
	for _, child := range f.Items.Fields {
		c.Template = append(c.Template, b.emit(child, name+".__i__."+child.Name)...)
	}
	b.spec = saved
	return c
}

// value reads the spec at a dotted name, indexing arrays by number.
func (b *builder) value(name string) any {
	var cur any = b.spec
	if b.spec == nil {
		return nil
	}
	for _, seg := range strings.Split(name, ".") {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return nil
			}
			cur = v[i]
		default:
			return nil
		}
		if cur == nil {
			return nil
		}
	}
	return cur
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// format renders a spec value for an input.
func format(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

func firstSentence(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

// acronyms are the field-name words rendered in capitals.
var acronyms = map[string]bool{
	"api": true, "url": true, "tls": true, "dht": true, "pex": true, "id": true, "uid": true, "rss": true, "crf": true,
	"hdr": true, "cq": true, "qsv": true, "nvenc": true, "hi": true, "ass": true, "pgs": true, "vip": true, "cc": true,
	"sdh": true, "ip": true, "ttl": true, "aq": true, "rc": true, "gpu": true, "cpu": true, "vmaf": true, "gss": true,
	"nzb": true, "nntp": true, "mb": true,
}

// Humanize turns a JSON field name into a label: camelCase split into
// words, a trailing Ref dropped, acronyms in capitals, the first word
// capitalised.
func Humanize(name string) string {
	name = strings.TrimSuffix(name, "Ref")
	var words []string
	var cur []rune
	runes := []rune(name)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			startsWord := unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev) ||
				(unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1])))
			if startsWord {
				words = append(words, string(cur))
				cur = nil
			}
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		words = append(words, string(cur))
	}
	for i, w := range words {
		letters := strings.Map(func(r rune) rune {
			if unicode.IsDigit(r) {
				return -1
			}
			return r
		}, w)
		switch {
		case acronyms[strings.ToLower(letters)]:
			words[i] = strings.ToUpper(w)
		case strings.ToUpper(w) == w && len(letters) > 1:
			// an acronym the camel split found ("DHT", "URL")
		case i == 0:
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		default:
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
}
