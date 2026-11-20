// Package manifest loads multi-document Kubernetes YAML into editable node
// trees and writes them back while preserving comments and key order.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// Object is a single Kubernetes resource inside a File.
type Object struct {
	Root   *yaml.Node // mapping node of the resource
	Source string
}

func (o *Object) Kind() string       { return yamlx.String(o.Root, "kind") }
func (o *Object) Name() string       { return yamlx.String(o.Root, "metadata", "name") }
func (o *Object) Namespace() string  { return yamlx.String(o.Root, "metadata", "namespace") }
func (o *Object) APIVersion() string { return yamlx.String(o.Root, "apiVersion") }

// Ref returns a human readable reference such as "Deployment/web".
func (o *Object) Ref() string {
	name := o.Name()
	if name == "" {
		name = "<unnamed>"
	}
	return o.Kind() + "/" + name
}

// Annotation returns metadata.annotations[key].
func (o *Object) Annotation(key string) string {
	return yamlx.String(o.Root, "metadata", "annotations", key)
}

// File is a parsed YAML stream.
type File struct {
	Source   string
	Docs     []*yaml.Node
	Objects  []*Object
	Writable bool // false for rendered Helm charts and cluster resources

	// chunks is the source split at "---" lines, so Encode can write
	// everything a fix doesn't change byte for byte: separators, comments,
	// empty documents and unmodified documents. nil when the stream could
	// not be split reliably.
	chunks []chunk
	// crlf is set when the input uses Windows line endings; re-encoded
	// documents then use them too, so a fix doesn't mix line endings.
	crlf bool
}

// chunk is the source text between two "---" lines.
type chunk struct {
	sep     []byte // the "---" line that starts the chunk, verbatim (nil for the first)
	text    []byte
	first   int    // file line the text starts on
	doc     int    // index in Docs, or -1 for a chunk with only comments or blank lines
	encoded []byte // how the unmodified document encodes
}

// Parse decodes a (possibly multi-document) YAML stream.
func Parse(data []byte, source string) (*File, error) {
	f := &File{Source: source, Writable: true}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		if len(doc.Content) == 0 {
			continue // an empty document has nothing to write back
		}
		// Keep every document, including lists and scalars, so --fix never
		// drops content it does not understand.
		d := doc
		f.Docs = append(f.Docs, &d)
		if d.Content[0].Kind == yaml.MappingNode {
			f.addObject(d.Content[0])
		}
	}
	f.chunks = splitDocuments(data, f.Docs)
	f.crlf = bytes.Count(data, []byte("\r\n")) > bytes.Count(data, []byte("\n"))/2
	return f, nil
}

var (
	docStart = regexp.MustCompile(`^---[ \t]*(#.*)?$`)
	// Markers this simple splitter does not understand: directives,
	// document end markers and content on the "---" line.
	docUnsupported = regexp.MustCompile(`^(%|\.\.\.|---[ \t]*[^ \t#])`)
	commentOnly    = regexp.MustCompile(`^\s*(#.*)?$`)
)

// splitDocuments splits data at "---" lines and pairs every decoded
// document with the chunk it came from. It returns nil when the pairing is
// not exact.
func splitDocuments(data []byte, docs []*yaml.Node) []chunk {
	type span struct{ first, last int } // 1-based line range of a chunk's text
	var chunks []chunk
	var spans []span
	lines := bytes.SplitAfter(data, []byte("\n"))
	cur, sp := chunk{doc: -1, first: 1}, span{first: 1}
	for i, l := range lines {
		n := i + 1
		trimmed := bytes.TrimRight(l, "\r\n")
		if docUnsupported.Match(trimmed) {
			return nil
		}
		if docStart.Match(trimmed) {
			sp.last = n - 1
			chunks, spans = append(chunks, cur), append(spans, sp)
			cur, sp = chunk{sep: l, doc: -1, first: n + 1}, span{first: n + 1}
			continue
		}
		cur.text = append(cur.text, l...)
	}
	sp.last = len(lines)
	chunks, spans = append(chunks, cur), append(spans, sp)

	// Spans are in order and their last lines strictly increase, so the
	// span ending at or after a line is found by binary search.
	endingAt := func(line int) int {
		return sort.Search(len(spans), func(i int) bool { return spans[i].last >= line })
	}
	for di, d := range docs {
		line := d.Content[0].Line
		ci := -1
		if i := endingAt(line); i < len(spans) && spans[i].first <= line {
			ci = i
		}
		// A comment-only document decodes to an empty null scalar positioned
		// at the next "---"; it belongs to the chunk that ends right before.
		if n := d.Content[0]; n.Kind == yaml.ScalarNode && n.Tag == "!!null" && n.Value == "" {
			if i := endingAt(line - 1); i < len(spans) && spans[i].last == line-1 && chunks[i].doc < 0 {
				ci = i
			}
		}
		if ci < 0 || chunks[ci].doc >= 0 {
			return nil
		}
		enc, err := encodeDoc(d)
		if err != nil {
			return nil
		}
		chunks[ci].doc, chunks[ci].encoded = di, enc
	}
	for _, c := range chunks {
		if c.doc >= 0 {
			continue
		}
		for _, l := range bytes.Split(c.text, []byte("\n")) {
			if !commentOnly.Match(l) {
				return nil
			}
		}
	}
	return chunks
}

func encodeDoc(d *yaml.Node) ([]byte, error) { return encodeStyled(d, false) }

// encodeStyled encodes d with 2-space indentation; compact writes block
// lists with the dash in the key's column ("key:\n- item").
func encodeStyled(d *yaml.Node, compact bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if compact {
		enc.CompactSeqIndent()
	}
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// compactLists reports whether the document's block lists are written in
// the compact style of kubectl and most Helm charts, with the dash in the
// key's column ("key:\n- item"), rather than indented ("key:\n  - item").
// Only nodes that came from the source count; a tie or a document without
// lists is compact.
func compactLists(n *yaml.Node) bool {
	compact, indented := 0, 0
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if v.Kind != yaml.SequenceNode || v.Style&yaml.FlowStyle != 0 || k.Line == 0 || v.Line <= k.Line {
					continue
				}
				if v.Column == k.Column {
					compact++
				} else {
					indented++
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(n)
	return compact >= indented
}

// keepBlockScalars switches block scalars that yaml.v3 cannot round-trip
// (a leading empty line is dropped) to double-quoted style.
func keepBlockScalars(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 && strings.HasPrefix(n.Value, "\n") {
		n.Style = yaml.DoubleQuotedStyle
	}
	for _, c := range n.Content {
		keepBlockScalars(c)
	}
}

func (f *File) addObject(root *yaml.Node) {
	o := &Object{Root: root, Source: f.Source}
	if strings.HasSuffix(o.Kind(), "List") {
		if items := yamlx.Get(root, "items"); items != nil && items.Kind == yaml.SequenceNode {
			for _, it := range items.Content {
				if it.Kind == yaml.MappingNode {
					f.addObject(it)
				}
			}
			return
		}
	}
	// Skip empty documents, k8s-guardian's own custom rule files and
	// Kustomize configuration (only meaningful when rendered).
	if o.Kind() == "" || strings.HasPrefix(o.APIVersion(), "k8s-guardian.io/") || strings.HasPrefix(o.APIVersion(), "kustomize.config.k8s.io/") {
		return
	}
	f.Objects = append(f.Objects, o)
}

// Encode renders the file back to YAML. Everything a fix did not change
// (separators, comments, empty documents, unmodified documents) is written
// exactly as it was read; modified documents are re-encoded.
func (f *File) Encode() ([]byte, error) {
	var buf bytes.Buffer
	if f.chunks != nil {
		for _, c := range f.chunks {
			buf.Write(c.sep)
			if c.doc < 0 {
				buf.Write(c.text)
				continue
			}
			d := f.Docs[c.doc]
			enc, err := encodeDoc(d)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(enc, c.encoded) {
				buf.Write(c.text)
				continue
			}
			keepBlockScalars(d)
			if patched := patchText(c.text, c.first, d, compactLists(d), f.newline()); patched != nil {
				buf.Write(patched)
				continue
			}
			if enc, err = f.reencode(d); err != nil {
				return nil, err
			}
			buf.Write(enc)
		}
		return buf.Bytes(), nil
	}
	for i, d := range f.Docs {
		if i > 0 {
			buf.WriteString("---")
			buf.Write(f.newline())
		}
		enc, err := f.reencode(d)
		if err != nil {
			return nil, err
		}
		buf.Write(enc)
	}
	return buf.Bytes(), nil
}

func (f *File) newline() []byte {
	if f.crlf {
		return []byte("\r\n")
	}
	return []byte("\n")
}

// reencode encodes a modified document with the file's line endings and
// the list style the document was written in.
func (f *File) reencode(d *yaml.Node) ([]byte, error) {
	keepBlockScalars(d)
	enc, err := encodeStyled(d, compactLists(d))
	if err != nil {
		return nil, err
	}
	if f.crlf {
		enc = bytes.ReplaceAll(enc, []byte("\n"), f.newline())
	}
	return enc, nil
}

// skipDirs are never walked: dependencies and VCS/tool metadata.
var skipDirs = map[string]bool{"node_modules": true, "vendor": true}

var looksLikeManifest = regexp.MustCompile(`(?m)^\s*apiVersion:`)

// LoadOptions customise how inputs are rendered.
type LoadOptions struct {
	// HelmValues (values files) and HelmSet (key=value) are passed to
	// `helm template` for a chart given as path itself, not for charts
	// found while walking a directory.
	HelmValues, HelmSet []string
	// KubeVersion is passed to `helm template --kube-version` for every
	// chart, so .Capabilities and kubeVersion constraints see the target
	// cluster instead of the version built into helm.
	KubeVersion string
	// Exclude, when set, skips files and directories during directory walks.
	Exclude func(path string) bool
}

// LoadWith reads manifests from a file, a directory (recursively), a Helm
// chart directory (rendered with `helm template`) or "-" for stdin, and
// returns non-fatal problems as warnings. Explicit files must parse. While
// walking a directory, YAML files that do not parse are skipped with a
// warning unless they look like Kubernetes manifests (a broken manifest must
// fail the check), and Helm charts that can't be rendered are skipped with a
// warning.
func LoadWith(path string, o LoadOptions) ([]*File, []string, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, nil, err
		}
		f, err := Parse(data, "<stdin>")
		if err != nil {
			return nil, nil, err
		}
		return []*File{f}, nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		f, err := loadFile(path)
		if err != nil {
			return nil, nil, err
		}
		return []*File{f}, nil, nil
	}
	if isChart(path) {
		f, err := renderChart(path, o)
		if err != nil {
			return nil, nil, err
		}
		return []*File{f}, nil, nil
	}
	if IsKustomization(path) {
		f, err := RenderKustomization(path)
		if err != nil {
			return nil, nil, err
		}
		return []*File{f}, nil, nil
	}
	// Files are read through an os.Root: a symlink (or a race replacing a
	// file with one) can never make the walk read outside the directory,
	// e.g. a pull request adding k8s/app.yaml -> ~/.kube/config. Symlinked
	// files are skipped so --fix never writes through them either.
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	var files []*File
	var warnings []string
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		p := filepath.Join(path, rel)
		if rel != "." && o.Exclude != nil && o.Exclude(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			warnings = append(warnings, fmt.Sprintf("skipped symlink %s", p))
			return nil
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] {
				return fs.SkipDir
			}
			if isChart(p) {
				f, err := renderChart(p, LoadOptions{KubeVersion: o.KubeVersion})
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("skipped Helm chart %s: %v", p, err))
				} else {
					files = append(files, f)
				}
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(rel))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := root.ReadFile(rel)
		if err != nil {
			return err
		}
		f, err := Parse(data, p)
		if err != nil {
			if looksLikeManifest.Match(data) {
				return err
			}
			warnings = append(warnings, fmt.Sprintf("skipped %s: not valid YAML (%v)", p, err))
			return nil
		}
		files = append(files, f)
		return nil
	})
	return files, warnings, err
}

func loadFile(path string) (*File, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a path the user passed explicitly
	if err != nil {
		return nil, err
	}
	return Parse(data, path)
}

// IsChart reports whether dir is a Helm chart.
func IsChart(dir string) bool { return isChart(dir) }

func isChart(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "Chart.yaml"))
	return err == nil
}

var kubeVersionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)(?:\.(\d+))?(?:[-+][0-9A-Za-z.+-]*)?$`)

// NormalizeKubeVersion turns "1.33", "v1.33.2" or a provider version such
// as "v1.29.4-gke.1043000" into "v1.33.0" / "v1.33.2" / "v1.29.4".
func NormalizeKubeVersion(v string) (string, error) {
	m := kubeVersionRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return "", fmt.Errorf("invalid Kubernetes version %q (want e.g. 1.33 or v1.33.2)", v)
	}
	patch := m[3]
	if patch == "" {
		patch = "0"
	}
	return "v" + m[1] + "." + m[2] + "." + patch, nil
}

// HelmArgs returns the `helm template` arguments for a chart. Values are
// passed as --flag=value so none of them can become another helm flag.
func HelmArgs(dir string, o LoadOptions) []string {
	args := []string{"template", "k8s-guardian", dir, "--include-crds"}
	for _, v := range o.HelmValues {
		args = append(args, "--values="+v)
	}
	for _, s := range o.HelmSet {
		args = append(args, "--set="+s)
	}
	if o.KubeVersion != "" {
		args = append(args, "--kube-version="+o.KubeVersion)
	}
	return args
}

func renderChart(dir string, o LoadOptions) (*File, error) {
	if _, err := exec.LookPath("helm"); err != nil {
		return nil, fmt.Errorf("%s is a Helm chart but `helm` was not found in PATH", dir)
	}
	var stderr bytes.Buffer
	cmd := exec.Command("helm", HelmArgs(dir, o)...) // #nosec G204 -- no shell; a local chart directory and --flag=value arguments
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helm template %s: %v: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	f, err := Parse(out, dir+" (helm template)")
	if err != nil {
		return nil, err
	}
	f.Writable = false
	return f, nil
}

var kustomizationFiles = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// IsKustomization reports whether dir contains a kustomization file.
func IsKustomization(dir string) bool {
	for _, name := range kustomizationFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// RenderKustomization builds a Kustomize directory with `kustomize build`,
// falling back to `kubectl kustomize`. Nested kustomizations inside a walked
// directory are not rendered automatically (bases and overlays would be
// reported twice); pass the overlay directory itself with -f or -k.
//
// Overlays that inflate Helm charts (the helmCharts field) only build with
// --enable-helm. The flag is added when the first build asks for it and helm
// is installed, so overlays without charts and kubectl versions that don't
// know the flag keep working as before.
func RenderKustomization(dir string) (*File, error) {
	var name string
	var args []string
	switch {
	case lookPath("kustomize"):
		name, args = "kustomize", []string{"build"}
	case lookPath("kubectl"):
		name, args = "kubectl", []string{"kustomize"}
	default:
		return nil, fmt.Errorf("%s is a Kustomize directory but neither `kustomize` nor `kubectl` was found in PATH", dir)
	}
	out, stderr, err := runKustomize(name, append(args, dir))
	if err != nil && strings.Contains(stderr, "--enable-helm") {
		if !lookPath("helm") {
			return nil, fmt.Errorf("%s uses helmCharts, which needs `helm` in PATH: %s %s %s: %v: %s", dir, name, strings.Join(args, " "), dir, err, stderr)
		}
		out, stderr, err = runKustomize(name, append(args, "--enable-helm", dir))
		args = append(args, "--enable-helm")
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s %s: %v: %s", name, strings.Join(args, " "), dir, err, stderr)
	}
	f, err := Parse(out, dir+" (kustomize)")
	if err != nil {
		return nil, err
	}
	f.Writable = false
	return f, nil
}

func runKustomize(name string, args []string) (out []byte, stderr string, err error) {
	cmd := exec.Command(name, args...) // #nosec G204 -- no shell; kustomize plugins stay disabled
	var buf bytes.Buffer
	cmd.Stderr = &buf
	out, err = cmd.Output()
	return out, strings.TrimSpace(buf.String()), err
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Identities returns "Kind/namespace/name" for every object, sorted, plus the
// number of non-object documents, so callers can detect when a rewrite
// added or dropped something.
func (f *File) Identities() string {
	var ids []string
	for _, o := range f.Objects {
		ids = append(ids, o.Kind()+"/"+o.Namespace()+"/"+o.Name())
	}
	sort.Strings(ids)
	other := len(f.Docs)
	for _, d := range f.Docs {
		if d.Content[0].Kind == yaml.MappingNode {
			other--
		}
	}
	return fmt.Sprintf("%s (+%d other documents)", strings.Join(ids, ", "), other)
}

// HasKind reports whether any object in f has the given kind.
func (f *File) HasKind(kind string) bool {
	for _, o := range f.Objects {
		if o.Kind() == kind {
			return true
		}
	}
	return false
}
