package policy_test

import (
	"io"
	"log/slog"
	"slices"
	"sort"
	"testing"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/artifactregistry"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/bigquery"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/build"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/cloudrun"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/gitlab"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/mcpx"
	"github.com/thehaymaker/kagent-porcelain/tools/policy"
)

func servers(t *testing.T) map[string]*mcpx.Server {
	mk := func(name string) *mcpx.Server {
		return mcpx.NewServer(mcpx.Options{Name: name, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}
	out := map[string]*mcpx.Server{}
	s := mk("gitlab-mcp")
	gitlab.Register(s, gitlab.NewClient(gitlab.Config{BaseURL: "https://g", AllowedGroup: "a", TemplateGroup: "b"}, nil, nil))
	out["gitlab-mcp"] = s
	s = mk("build-mcp")
	bc := build.Config{Project: "wb-builds", Location: "europe-west1", Repository: "builds", Bucket: "wb-src", ServiceAccount: "b@wb-builds.iam.gserviceaccount.com"}
	if err := bc.Validate(); err != nil {
		t.Fatal(err)
	}
	build.Register(s, build.NewClient(bc, nil, nil))
	out["build-mcp"] = s
	s = mk("cloudrun-mcp")
	crc := cloudrun.Config{PreviewProject: "wb-preview", Region: "europe-west1", ImagePrefix: "europe-west1-docker.pkg.dev/p/r/"}
	if err := crc.Validate(); err != nil {
		t.Fatal(err)
	}
	cloudrun.Register(s, cloudrun.NewClient(crc, nil))
	out["cloudrun-mcp"] = s
	s = mk("artifactregistry-mcp")
	arc := artifactregistry.Config{Project: "wb-builds", Location: "europe-west1", Repository: "builds"}
	_ = arc.Validate()
	artifactregistry.Register(s, artifactregistry.NewClient(arc, nil))
	out["artifactregistry-mcp"] = s
	s = mk("bigquery-mcp")
	bqc := bigquery.Config{Project: "wb-data", Location: "EU", MaxBytesBilled: 1}
	_ = bqc.Validate()
	bigquery.Register(s, bigquery.NewClient(bqc, nil))
	out["bigquery-mcp"] = s
	return out
}

func TestCatalogMatchesServersAndAnnotations(t *testing.T) {
	for name, s := range servers(t) {
		var got []string
		for _, tl := range s.Tools() {
			got = append(got, tl.Name)
			classes := policy.Classify(tl.Name, nil)
			a := tl.Annotations
			destructive := a.DestructiveHint == nil || *a.DestructiveHint
			if slices.Contains(classes, policy.DataDeletion) || slices.Contains(classes, policy.IAMChange) {
				if a.ReadOnlyHint || !destructive {
					t.Errorf("%s is %v but not annotated destructive", tl.Name, classes)
				}
			}
			if a.ReadOnlyHint && len(classes) > 0 {
				t.Errorf("%s is read-only but classified %v", tl.Name, classes)
			}
		}
		want := slices.Clone(policy.Tools[name])
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: registered %v, catalog %v", name, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want []policy.Class
	}{
		{"bigquery_delete_table", nil, []policy.Class{policy.DataDeletion}},
		{"cloudrun_delete_preview_service", nil, []policy.Class{policy.DataDeletion}},
		{"cloudrun_set_iam_invoker", nil, []policy.Class{policy.IAMChange}},
		{"cloudrun_share_preview", nil, []policy.Class{policy.IAMChange}},
		{"bigquery_create_dataset", nil, []policy.Class{policy.NewSpend}},
		{"cloudrun_deploy_preview", map[string]any{"allow_create": true}, []policy.Class{policy.NewSpend}},
		{"cloudrun_deploy_preview", map[string]any{"image": "x"}, nil},
		{"cloudrun_deploy_preview", nil, []policy.Class{policy.NewSpend}},
		{"bigquery_run_query", nil, nil},
		{"gitlab_commit_files", nil, nil},
	}
	for _, c := range cases {
		if got := policy.Classify(c.tool, c.args); !slices.Equal(got, c.want) {
			t.Errorf("Classify(%s, %v) = %v, want %v", c.tool, c.args, got, c.want)
		}
	}
}
