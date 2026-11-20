package manifest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

const stream = `# header comment
---
apiVersion: v1
kind: ConfigMap
metadata: {name: cfg}
data:
  config.yaml: |

    starts: with an empty line
  filters: >-
    [a,b]
    [c,d]
---
# Source: chart/templates/pdb.yaml
# a template that rendered to comments only
---
# Source: chart/templates/deploy.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
      - name: web   # inline comment
        image: nginx:1.27
        args:
        - --port=8080
`

func parse(t *testing.T, s string) *File {
	t.Helper()
	f, err := Parse([]byte(s), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func encode(t *testing.T, f *File) string {
	t.Helper()
	out, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestEncodeKeepsUnmodifiedDocumentsVerbatim(t *testing.T) {
	f := parse(t, stream)
	if got := encode(t, f); got != stream {
		t.Fatalf("round trip changed the stream:\n%s", got)
	}
}

func TestEncodeRewritesOnlyModifiedDocuments(t *testing.T) {
	f := parse(t, stream)
	var deploy *Object
	for _, o := range f.Objects {
		if o.Kind() == "Deployment" {
			deploy = o
		}
	}
	yamlx.Set(yamlx.Path(deploy.Root, "metadata"), "namespace", yamlx.Str("prod"))
	got := encode(t, f)
	cm := strings.SplitN(got, "---\n", 3)[1]
	if !strings.Contains(cm, "  config.yaml: |\n\n    starts") || !strings.Contains(cm, "    [a,b]\n    [c,d]\n") {
		t.Errorf("unmodified ConfigMap was reformatted:\n%s", cm)
	}
	if !strings.Contains(got, "namespace: prod") || !strings.Contains(got, "# inline comment") {
		t.Errorf("modified document lost the change or its comments:\n%s", got)
	}
}

func TestEncodeKeepsLeadingNewlineInModifiedBlockScalar(t *testing.T) {
	f := parse(t, stream)
	cm := f.Objects[0]
	yamlx.Set(yamlx.Path(cm.Root, "metadata"), "namespace", yamlx.Str("prod"))
	var back struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(strings.SplitN(encode(t, f), "---\n", 3)[1]), &back); err != nil {
		t.Fatal(err)
	}
	if want := "\nstarts: with an empty line\n"; back.Data["config.yaml"] != want {
		t.Errorf("config.yaml = %q, want %q", back.Data["config.yaml"], want)
	}
}

func TestEncodeKeepsSeparatorsAndEmptyDocuments(t *testing.T) {
	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: a}\n"
	for _, in := range []string{
		"---\n# Source: c/templates/empty.yaml\n---\n# Source: c/templates/cm.yaml\n" + cm, // helm template output
		cm + "---\n",
		cm + "---\n---\n" + cm,
		"--- # first\n" + cm + "---\t\n" + cm,
		"# only a comment\n",
		"---",
		"",
	} {
		f := parse(t, in)
		if got := encode(t, f); got != in {
			t.Errorf("round trip changed %q to %q", in, got)
		}
	}
	// A fix still keeps the separators and the empty documents around it.
	f := parse(t, "---\n# Source: c/templates/empty.yaml\n---\n"+cm+"---\n")
	yamlx.Set(yamlx.Path(f.Objects[0].Root, "metadata"), "namespace", yamlx.Str("prod"))
	want := "---\n# Source: c/templates/empty.yaml\n---\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: a, namespace: prod}\n---\n"
	if got := encode(t, f); got != want {
		t.Errorf("fixed stream:\n%q\nwant\n%q", got, want)
	}
}

func TestEncodeFallsBackForUnusualStreams(t *testing.T) {
	// Content on the "---" line can't be split by line; everything is re-encoded.
	f := parse(t, "--- {kind: ConfigMap, apiVersion: v1, metadata: {name: a}}\n---\nkind: Secret\napiVersion: v1\nmetadata: {name: b}\n")
	if f.chunks != nil {
		t.Fatal("expected the verbatim mode to be disabled")
	}
	if got := encode(t, f); !strings.Contains(got, "name: a") || !strings.Contains(got, "name: b") {
		t.Errorf("documents lost:\n%s", got)
	}
}

func TestHelmArgsCannotInjectFlags(t *testing.T) {
	got := strings.Join(HelmArgs("chart", LoadOptions{HelmValues: []string{"--post-renderer=/bin/sh"}, HelmSet: []string{"a=b"}}), " ")
	if got != "template k8s-guardian chart --include-crds --values=--post-renderer=/bin/sh --set=a=b" {
		t.Errorf("unexpected helm args: %s", got)
	}
}

func TestNormalizeKubeVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.33": "v1.33.0", "v1.33": "v1.33.0", "1.33.2": "v1.33.2", "v1.29.4-gke.1043000": "v1.29.4", " v1.30.1+k3s1 ": "v1.30.1",
	} {
		if got, err := NormalizeKubeVersion(in); err != nil || got != want {
			t.Errorf("NormalizeKubeVersion(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "latest", "1", "1.x", "--post-renderer=/bin/sh", "1.33;rm"} {
		if _, err := NormalizeKubeVersion(bad); err == nil {
			t.Errorf("NormalizeKubeVersion(%q) accepted", bad)
		}
	}
	got := strings.Join(HelmArgs("chart", LoadOptions{KubeVersion: "v1.33.0"}), " ")
	if got != "template k8s-guardian chart --include-crds --kube-version=v1.33.0" {
		t.Errorf("unexpected helm args: %s", got)
	}
}

func TestEncodeKeepsWindowsLineEndings(t *testing.T) {
	in := "apiVersion: v1\r\nkind: ConfigMap\r\nmetadata: {name: cfg}\r\ndata: {a: b}\r\n---\r\napiVersion: v1\r\nkind: Pod\r\nmetadata: {name: p}\r\nspec:\r\n  containers: [{name: c, image: nginx}]\r\n"
	f := parse(t, in)
	if got := encode(t, f); got != in {
		t.Fatalf("round trip changed a CRLF file:\n%q", got)
	}
	yamlx.Set(yamlx.Path(f.Objects[1].Root, "metadata"), "namespace", yamlx.Str("prod"))
	got := encode(t, f)
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Errorf("mixed line endings after a change:\n%q", got)
	}
	if !strings.HasPrefix(got, "apiVersion: v1\r\nkind: ConfigMap\r\nmetadata: {name: cfg}\r\ndata: {a: b}\r\n---\r\n") || !strings.Contains(got, "metadata: {name: p, namespace: prod}\r\n") {
		t.Errorf("unexpected output:\n%q", got)
	}
}

// fixed parses src, applies change to its last document and encodes it.
func fixed(t *testing.T, src string, change func(doc *yaml.Node)) string {
	t.Helper()
	f := parse(t, src)
	change(f.Docs[len(f.Docs)-1].Content[0])
	return encode(t, f)
}

func container(doc *yaml.Node) *yaml.Node {
	return yamlx.Items(yamlx.Path(doc, "spec", "template", "spec", "containers"))[0]
}

func TestFixChangesOnlyItsLines(t *testing.T) {
	const other = "# Source: chart/templates/cm.yaml\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: a}\n---\n"
	const deploy = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
      - name: web   # the app
        image: r.io/web:1
        args:
          - --port=8080
        securityContext:
          allowPrivilegeEscalation: true # was needed once
          readOnlyRootFilesystem:  false
        resources: {}
      - name: sidecar
        image: r.io/sidecar:1
        resources:
          {}
      - name: third
        image: r.io/third:1
        resources:
          limits: null
`
	for _, tc := range []struct {
		name   string
		change func(doc *yaml.Node)
		want   string // deploy with the expected edits
	}{
		{"new keys go above the first key on its own line", func(d *yaml.Node) {
			yamlx.Set(yamlx.EnsureMap(yamlx.Path(d, "spec", "template", "spec"), "securityContext"), "runAsNonRoot", yamlx.Bool(true))
			yamlx.Set(container(d), "imagePullPolicy", yamlx.Str("IfNotPresent"))
		}, strings.NewReplacer(
			"    spec:\n      containers:", "    spec:\n      securityContext:\n        runAsNonRoot: true\n      containers:",
			"      - name: web   # the app\n        image: r.io/web:1", "      - name: web   # the app\n        imagePullPolicy: IfNotPresent\n        image: r.io/web:1",
		).Replace(deploy)},
		{"a changed scalar is replaced in place, its comment kept", func(d *yaml.Node) {
			sc := yamlx.Get(container(d), "securityContext")
			yamlx.Set(sc, "allowPrivilegeEscalation", yamlx.Bool(false))
			yamlx.Set(sc, "readOnlyRootFilesystem", yamlx.Bool(true))
		}, strings.NewReplacer(
			"allowPrivilegeEscalation: true # was", "allowPrivilegeEscalation: false # was",
			"readOnlyRootFilesystem:  false", "readOnlyRootFilesystem:  true",
		).Replace(deploy)},
		{"{} and null values are filled below their key", func(d *yaml.Node) {
			for _, c := range yamlx.Items(yamlx.Path(d, "spec", "template", "spec", "containers")) {
				res := yamlx.EnsureMap(c, "resources")
				yamlx.Set(yamlx.EnsureMap(res, "limits"), "memory", yamlx.Str("128Mi"))
			}
		}, strings.NewReplacer(
			"        resources: {}\n", "        resources:\n          limits:\n            memory: 128Mi\n",
			"        resources:\n          {}\n", "        resources:\n          limits:\n            memory: 128Mi\n",
			"          limits: null\n", "          limits:\n            memory: 128Mi\n",
		).Replace(deploy)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The Deployment is the second document, so its positions are not
			// the positions in its own text.
			if got := fixed(t, other+deploy, tc.change); got != other+tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, other+tc.want)
			}
		})
	}
}

func TestFixFallsBackToReencoding(t *testing.T) {
	// Removing a key can't be written as an edit: the document is re-encoded
	// in its own (compact) list style.
	got := fixed(t, "kind: Pod\nspec:\n  containers:\n  - name: a\n    securityContext: {runAsUser: 0}\n", func(d *yaml.Node) {
		c := yamlx.Items(yamlx.Path(d, "spec", "containers"))[0]
		yamlx.Delete(yamlx.Get(c, "securityContext"), "runAsUser")
		yamlx.Set(yamlx.Get(c, "securityContext"), "runAsNonRoot", yamlx.Bool(true))
	})
	if want := "kind: Pod\nspec:\n  containers:\n  - name: a\n    securityContext: {runAsNonRoot: true}\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFixKeepsWindowsLineEndingsInEdits(t *testing.T) {
	src := "kind: Pod\r\nspec:\r\n  containers:\r\n  - name: a\r\n    image: x\r\n"
	got := fixed(t, src, func(d *yaml.Node) {
		yamlx.Set(yamlx.Items(yamlx.Path(d, "spec", "containers"))[0], "imagePullPolicy", yamlx.Str("Always"))
	})
	if want := "kind: Pod\r\nspec:\r\n  containers:\r\n  - name: a\r\n    imagePullPolicy: Always\r\n    image: x\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCompactLists(t *testing.T) {
	for src, want := range map[string]bool{
		"a:\n- x\nb:\n- y\n":            true,
		"a:\n  - x\nb:\n  - y\n":        false,
		"a:\n  - x\nb:\n- y\nc:\n- z\n": true,
		"a: [x]\nb: c\n":                true, // no block lists
	} {
		var d yaml.Node
		if err := yaml.Unmarshal([]byte(src), &d); err != nil {
			t.Fatal(err)
		}
		if got := compactLists(&d); got != want {
			t.Errorf("compactLists(%q) = %v, want %v", src, got, want)
		}
	}
}

// A chart's crds/ directory is part of what helm install creates, so it is
// part of what gets checked (and LV001 sees the CRDs a chart ships).
func TestHelmIncludesChartCRDs(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../cli/testdata/chart")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "crds"), 0o750); err != nil {
		t.Fatal(err)
	}
	crd := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata: {name: widgets.example.com}\nspec:\n  group: example.com\n  names: {kind: Widget, plural: widgets}\n  scope: Namespaced\n  versions: [{name: v1, served: true, storage: true, schema: {openAPIV3Schema: {type: object}}}]\n"
	if err := os.WriteFile(filepath.Join(dir, "crds", "widget.yaml"), []byte(crd), 0o600); err != nil {
		t.Fatal(err)
	}
	files, _, err := LoadWith(dir, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, f := range files {
		for _, o := range f.Objects {
			kinds = append(kinds, o.Kind())
		}
	}
	if strings.Join(kinds, ",") != "CustomResourceDefinition,Deployment" {
		t.Errorf("rendered objects: %v", kinds)
	}
}

// writeScript writes an executable shell script named name into dir.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o700); err != nil { // #nosec G306 -- test helper must be executable
		t.Fatal(err)
	}
}

func TestRenderKustomizationEnablesHelm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl is a shell script")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	// Like kustomize: overlays with helmCharts fail without --enable-helm.
	writeScript(t, bin, "kubectl", `case "$*" in
  "kustomize --enable-helm overlay") printf 'apiVersion: v1\nkind: Pod\nmetadata: {name: from-chart}\n' ;;
  "kustomize overlay") echo 'Error: trouble configuring builtin HelmChartInflationGenerator: must specify --enable-helm' >&2; exit 1 ;;
  *) echo "unexpected: $*" >&2; exit 2 ;;
esac
`)
	_, err := RenderKustomization("overlay")
	if err == nil || !strings.Contains(err.Error(), "overlay uses helmCharts, which needs `helm` in PATH") {
		t.Fatalf("without helm: %v", err)
	}
	writeScript(t, bin, "helm", "exit 0\n")
	f, err := RenderKustomization("overlay")
	if err != nil || len(f.Objects) != 1 || f.Objects[0].Name() != "from-chart" || f.Writable {
		t.Fatalf("with helm: %v %+v", err, f)
	}
}

// TestRenderKustomizationHelmCharts runs the real tools on an overlay that
// inflates a local chart.
func TestRenderKustomizationHelmCharts(t *testing.T) {
	if !lookPath("helm") || !lookPath("kustomize") {
		t.Skip("helm or kustomize not installed")
	}
	dir := t.TempDir()
	if err := os.CopyFS(filepath.Join(dir, "charts", "web"), os.DirFS("../cli/testdata/chart")); err != nil {
		t.Fatal(err)
	}
	kust := "helmCharts:\n  - name: web\n    releaseName: web\n    valuesInline: {replicas: 3}\n"
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kust), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := RenderKustomization(dir)
	if err != nil {
		t.Fatal(err)
	}
	// valuesInline from the kustomization reaches the chart.
	if len(f.Objects) != 1 {
		t.Fatalf("rendered %d objects", len(f.Objects))
	}
	if o := f.Objects[0]; o.Kind() != "Deployment" || yamlx.String(o.Root, "spec", "replicas") != "3" {
		t.Fatalf("rendered %s with replicas %q", o.Ref(), yamlx.String(o.Root, "spec", "replicas"))
	}
}
