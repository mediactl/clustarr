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

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"sigs.k8s.io/yaml"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

const (
	formatYAML = "yaml"
	formatCRD  = "crd"

	// definitionsDir is the schema version the engine implements.
	definitionsDir = "definitions/v11"

	// maxArchiveBytes caps the compressed tarball read. The whole
	// repository at a commit (every schema version) is a few tens of MB.
	maxArchiveBytes = 256 << 20
	// maxSchemaBytes caps schema.json; the v11 schema is 34 KB.
	maxSchemaBytes = 4 << 20
	// maxReasonBytes bounds one refused file's reason in SOURCE.txt.
	maxReasonBytes = 400
)

// licenceNotice is printed on every run and heads SOURCE.txt.
const licenceNotice = `sync-cardigann: Prowlarr/Indexers carries no licence (its history is Jackett's
GPL-2.0 tree, whose LICENSE was deleted in 2020). These definitions are fetched
for your own cluster; they are not part of Clustarr and must not be committed
to its source tree. See hack/sync-cardigann's package documentation.`

// options is one run's flags.
type options struct {
	Commit, URL, Src, Out, Format string
}

// report is what one run found and wrote.
type report struct {
	Source      string
	Upstream    int   // v11 definition files found
	UpstreamB   int64 // their bytes
	Accepted    []cardigann.BundledDefinition
	AcceptedB   int64
	Refused     []cardigann.BundleIssue
	SchemaDrift bool
}

// Summary is the one line printed on success.
func (r report) Summary() string {
	drift := ""
	if r.SchemaDrift {
		drift = "; WARNING: upstream schema.json differs from pkg/cardigann's"
	}
	return fmt.Sprintf("%s: %d v11 definitions (%d bytes); accepted %d (%d bytes), refused %d%s",
		r.Source, r.Upstream, r.UpstreamB, len(r.Accepted), r.AcceptedB, len(r.Refused), drift)
}

// run is main without the process: read the corpus, load it, write it.
func run(opts options, client *http.Client) (report, error) {
	switch opts.Format {
	case formatYAML, formatCRD:
	default:
		return report{}, fmt.Errorf("-format %q: want %q or %q", opts.Format, formatYAML, formatCRD)
	}
	if err := ensureEmptyDir(opts.Out); err != nil {
		return report{}, err
	}

	var (
		files  fstest.MapFS
		schema []byte
		err    error
		rep    report
	)
	if opts.Src != "" {
		rep.Source = "local checkout " + opts.Src
		files, schema, err = readDir(filepath.Join(opts.Src, filepath.FromSlash(definitionsDir)))
	} else {
		rep.Source = "Prowlarr/Indexers@" + opts.Commit
		files, schema, err = fetchArchive(client, fmt.Sprintf(opts.URL, opts.Commit))
	}
	if err != nil {
		return report{}, err
	}
	for _, f := range files {
		rep.Upstream++
		rep.UpstreamB += int64(len(f.Data))
	}
	rep.SchemaDrift = schema != nil && !bytes.Equal(schema, cardigann.EmbeddedSchema())

	rep.Accepted, rep.Refused, err = cardigann.LoadBundle(files)
	if err != nil {
		return report{}, err
	}
	if opts.Format == formatCRD {
		rep.Accepted, rep.Refused = dedupeNames(rep.Accepted, rep.Refused)
	}
	for _, d := range rep.Accepted {
		rep.AcceptedB += int64(len(d.YAML))
	}
	if err := write(opts, rep); err != nil {
		return report{}, err
	}
	return rep, nil
}

// ensureEmptyDir creates dir, or accepts it when it exists and is empty. A
// non-empty directory is refused rather than merged into: a definition
// upstream removed must not linger from an earlier run.
func ensureEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return os.MkdirAll(dir, 0o755)
	case err != nil:
		return fmt.Errorf("-out %s: %w", dir, err)
	case len(entries) > 0:
		return fmt.Errorf("-out %s is not empty; remove it or choose another directory", dir)
	}
	return nil
}

// readDir reads every *.yml in dir, and its schema.json.
func readDir(dir string) (fstest.MapFS, []byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", dir, err)
	}
	files := fstest.MapFS{}
	var schema []byte
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name != "schema.json" && path.Ext(name) != ".yml" {
			continue
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		data, rerr := readEntry(f, name)
		_ = f.Close()
		if rerr != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, rerr)
		}
		if name == "schema.json" {
			schema = data
			continue
		}
		files[name] = &fstest.MapFile{Data: data}
	}
	return files, schema, nil
}

// fetchArchive downloads the repository tarball and keeps definitions/v11:
// every *.yml directly in it, and schema.json. GitHub prefixes every path
// with "<repo>-<commit>/", which is stripped.
func fetchArchive(client *http.Client, url string) (fstest.MapFS, []byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	capped := &countingReader{r: io.LimitReader(resp.Body, maxArchiveBytes+1)}
	gz, err := gzip.NewReader(capped)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	tr := tar.NewReader(gz)
	files := fstest.MapFS{}
	var schema []byte
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if capped.n > maxArchiveBytes {
				return nil, nil, fmt.Errorf("fetch %s: archive exceeds %d bytes", url, maxArchiveBytes)
			}
			return nil, nil, fmt.Errorf("fetch %s: %w", url, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel := hdr.Name
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			rel = rel[i+1:]
		}
		dir, name := path.Split(rel)
		if strings.TrimSuffix(dir, "/") != definitionsDir {
			continue
		}
		if name != "schema.json" && path.Ext(name) != ".yml" {
			continue
		}
		data, err := readEntry(tr, name)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", rel, err)
		}
		if name == "schema.json" {
			schema = data
			continue
		}
		files[name] = &fstest.MapFile{Data: data}
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("fetch %s: no %s/*.yml in the archive", url, definitionsDir)
	}
	return files, schema, nil
}

// countingReader counts what it has read, so a truncated archive can be told
// apart from a corrupt one.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// readEntry reads one file. schema.json over maxSchemaBytes fails the run; a
// definition is read to one byte past cardigann.MaxDefinitionBytes and no
// further, so an oversized one reaches LoadBundle -- which refuses it as
// one file among many -- without this tool buffering all of it.
func readEntry(r io.Reader, name string) ([]byte, error) {
	if name != "schema.json" {
		return io.ReadAll(io.LimitReader(r, cardigann.MaxDefinitionBytes+1))
	}
	data, err := io.ReadAll(io.LimitReader(r, maxSchemaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSchemaBytes {
		return nil, fmt.Errorf("exceeds %d bytes", maxSchemaBytes)
	}
	return data, nil
}

// objectName turns a definition id into an IndexerDefinition name: the one
// rule both producers of bundle objects share, cardigann.ObjectName, so the
// manifests this writes and the objects indexarr's bundle loader creates
// from the same corpus have the same names.
func objectName(id string) string { return cardigann.ObjectName(id) }

// dedupeNames refuses a definition whose object name another accepted
// definition already has (ids differing only in case).
func dedupeNames(defs []cardigann.BundledDefinition, issues []cardigann.BundleIssue) ([]cardigann.BundledDefinition, []cardigann.BundleIssue) {
	seen := map[string]string{}
	out := defs[:0:0]
	for _, d := range defs {
		name := objectName(d.ID())
		if name == "" {
			issues = append(issues, cardigann.BundleIssue{File: d.File, Err: fmt.Errorf("id %q yields no valid object name", d.ID())})
			continue
		}
		if first, dup := seen[name]; dup {
			issues = append(issues, cardigann.BundleIssue{File: d.File, Err: fmt.Errorf("object name %q already used by %s", name, first)})
			continue
		}
		seen[name] = d.File
		out = append(out, d)
	}
	return out, issues
}

// indexerDefinition is the manifest -format crd writes: only the fields an
// operator applies, so no creationTimestamp or empty status is emitted.
type indexerDefinition struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		YAML string `json:"yaml"`
	} `json:"spec"`
}

// write puts the accepted definitions and SOURCE.txt into opts.Out.
func write(opts options, rep report) error {
	for _, d := range rep.Accepted {
		var (
			name = d.File
			data = d.YAML
		)
		if opts.Format == formatCRD {
			var obj indexerDefinition
			obj.APIVersion, obj.Kind = "index.clustarr.io/v1alpha1", "IndexerDefinition"
			obj.Metadata.Name = objectName(d.ID())
			obj.Spec.YAML = string(d.YAML)
			body, err := yaml.Marshal(obj)
			if err != nil {
				return fmt.Errorf("%s: %w", d.File, err)
			}
			header := fmt.Sprintf("# %s, %s/%s. Not part of Clustarr; see hack/sync-cardigann.\n",
				rep.Source, definitionsDir, d.File)
			name = obj.Metadata.Name + ".yaml"
			data = append([]byte(header), body...)
		}
		if err := os.WriteFile(filepath.Join(opts.Out, name), data, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(opts.Out, "SOURCE.txt"), sourceText(opts, rep), 0o644)
}

// sourceText is SOURCE.txt: provenance, the licence position, the counts,
// and every refused file with its (bounded) reason.
func sourceText(opts options, rep report) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", licenceNotice)
	fmt.Fprintf(&b, "source:    %s\n", rep.Source)
	fmt.Fprintf(&b, "fetched:   %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "format:    %s\n", opts.Format)
	fmt.Fprintf(&b, "summary:   %s\n", rep.Summary())
	if rep.SchemaDrift {
		b.WriteString("schema:    upstream definitions/v11/schema.json differs from pkg/cardigann/schema.json\n")
	}
	refused := append([]cardigann.BundleIssue(nil), rep.Refused...)
	sort.Slice(refused, func(i, j int) bool { return refused[i].File < refused[j].File })
	fmt.Fprintf(&b, "\nrefused (%d):\n", len(refused))
	for _, i := range refused {
		reason := strings.Join(strings.Fields(i.Err.Error()), " ")
		if len(reason) > maxReasonBytes {
			cut := maxReasonBytes
			for cut > 0 && !utf8.RuneStart(reason[cut]) {
				cut--
			}
			reason = reason[:cut] + " ..."
		}
		fmt.Fprintf(&b, "  %s: %s\n", i.File, reason)
	}
	return []byte(b.String())
}
