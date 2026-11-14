package components

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedCatalogMatchesRepository(t *testing.T) {
	repo, err := os.ReadFile("../../../catalogue/catalog.json")
	if err != nil {
		t.Skip("repository catalogue not found")
	}
	if !bytes.Equal(repo, Catalog) {
		t.Fatal("server/internal/components/catalog.json is out of date; copy catalogue/catalog.json over it")
	}
}

func TestExamplesValidate(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../../../catalogue/examples/*.jsonl")
	if len(files) == 0 {
		t.Skip("no examples")
	}
	for _, f := range files {
		data, _ := os.ReadFile(f)
		var msgs []map[string]any
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			msgs = append(msgs, m)
		}
		if _, err := v.Validate(msgs); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
	}
}

func parse(t *testing.T, s string) []map[string]any {
	t.Helper()
	var m []map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRejects(t *testing.T) {
	v, _ := NewValidator()
	cat := `"https://workbench.dev/catalogs/workbench/v1/catalog.json"`
	cases := map[string]string{
		"unknown component": `[{"updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Iframe","src":"x"}]}}]`,
		"unknown property":  `[{"updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Text","text":"x","html":"<b>"}]}}]`,
		"bad enum":          `[{"updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Text","text":"x","variant":"blink"}]}}]`,
		"http link":         `[{"updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Link","label":"x","url":"javascript:alert(1)"}]}}]`,
		"missing required":  `[{"updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"ApprovalCard"}]}}]`,
		"wrong catalogue":   `[{"createSurface":{"surfaceId":"s","catalogId":"https://evil/catalog.json"}}]`,
		"two surfaces":      `[{"createSurface":{"surfaceId":"a","catalogId":` + cat + `}},{"deleteSurface":{"surfaceId":"b"}}]`,
		"two kinds":         `[{"createSurface":{"surfaceId":"a","catalogId":` + cat + `},"deleteSurface":{"surfaceId":"a"}}]`,
		"wrong version":     `[{"version":"v0.8","deleteSurface":{"surfaceId":"a"}}]`,
		"duplicate ids":     `[{"updateComponents":{"surfaceId":"s","components":[{"id":"a","component":"Divider"},{"id":"a","component":"Divider"}]}}]`,
		"bad binding":       `[{"updateComponents":{"surfaceId":"s","components":[{"id":"a","component":"Text","text":{"path":"no-slash"}}]}}]`,
		"bad step state":    `[{"updateComponents":{"surfaceId":"s","components":[{"id":"a","component":"Progress","steps":[{"label":"x","state":"exploded"}]}]}}]`,
		"empty":             `[]`,
	}
	for name, c := range cases {
		if _, err := v.Validate(parse(t, c)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAcceptsBindingAndStampsVersion(t *testing.T) {
	v, _ := NewValidator()
	b, err := v.Validate(parse(t, `[{"updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Text","text":{"path":"/greeting"},"weight":1}]}},{"updateDataModel":{"surfaceId":"s","path":"/greeting","value":"hi"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if b.SurfaceID != "s" || b.Messages[0]["version"] != Version {
		t.Fatalf("%+v", b)
	}
}
