package policy

import "testing"

func TestDefaultValid(t *testing.T) {
	if err := Default("approvers").Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestClassify(t *testing.T) {
	p := Default("approvers")
	cases := []struct {
		tool string
		args map[string]any
		want Kind
	}{
		{"workbench_request_promotion", nil, ProductionDeploy},
		{"kagent__workbench_request_promotion", nil, ProductionDeploy},
		{"cloudrun_deploy", map[string]any{"environment": "Production"}, ProductionDeploy},
		{"cloudrun_deploy", map[string]any{"environment": "preview"}, Ordinary},
		{"cloudrun_set_iam_invoker", nil, IAMChange},
		{"bigquery_delete_table", nil, DataDeletion},
		{"bigquery_create_dataset", nil, NewSpend},
		{"cloudrun_deploy_preview", map[string]any{"allow_create": true}, NewSpend},
		{"cloudrun_deploy_preview", map[string]any{"allow_create": false}, Ordinary},
		{"gitlab_commit_files", nil, Ordinary},
		{"read_file", nil, Ordinary},
	}
	for _, c := range cases {
		if got := p.Classify(c.tool, c.args); got != c.want {
			t.Errorf("Classify(%q, %v) = %s, want %s", c.tool, c.args, got, c.want)
		}
	}
}

func TestMostRestrictiveWins(t *testing.T) {
	// A tool matching both deletion and IAM patterns is an IAM change.
	p := Default("approvers")
	if got := p.Classify("iam_delete_binding", nil); got != IAMChange {
		t.Fatalf("got %s", got)
	}
	if Most(Ordinary, NewSpend, ProductionDeploy, DataDeletion) != ProductionDeploy {
		t.Fatal("Most")
	}
}

func TestRequesterCannotApproveOwnProductionDeploy(t *testing.T) {
	p := Default("approvers")
	r := p.Route(ProductionDeploy, "alice")
	if r.MayDecide(Decider{ID: "alice", Groups: []string{"approvers"}}) {
		t.Fatal("requester must not decide their own production deploy, even as an approver")
	}
	if !r.MayDecide(Decider{ID: "bob", Groups: []string{"approvers"}}) {
		t.Fatal("an approver must be able to decide")
	}
	if r.MayDecide(Decider{ID: "carol", Groups: []string{"requesters"}}) {
		t.Fatal("someone outside the group must not decide")
	}
	r.ApproverIDs = []string{"carol"}
	if !r.MayDecide(Decider{ID: "carol"}) {
		t.Fatal("a named approver must be able to decide")
	}
	if r.MayDecide(Decider{}) {
		t.Fatal("an anonymous decider must never decide")
	}
}

func TestOrdinaryIsRequesterOnly(t *testing.T) {
	r := Default("approvers").Route(Ordinary, "alice")
	if !r.MayDecide(Decider{ID: "alice"}) || r.MayDecide(Decider{ID: "bob", Groups: []string{"approvers"}}) {
		t.Fatal("ordinary calls are the requester's to decide")
	}
}

func TestValidateRejectsUndecidable(t *testing.T) {
	p := Policy{Kinds: []KindPolicy{{Kind: IAMChange, Rules: []Rule{{Tools: []string{"x"}}}}}}
	if err := p.Validate(); err == nil {
		t.Fatal("a kind nobody can decide must be rejected")
	}
	p = Policy{Kinds: []KindPolicy{{Kind: Ordinary, Rules: []Rule{{Tools: []string{"x"}}}, RequesterMayDecide: true}}}
	if err := p.Validate(); err == nil {
		t.Fatal("ordinary is not configurable")
	}
}

// TestGCPToolServerCatalog pins how the default policy classifies every tool
// the GCP tool servers (tools/) expose. If a tool is renamed there, this
// fails rather than letting an always-ask action become ordinary.
func TestGCPToolServerCatalog(t *testing.T) {
	p := Default("approvers")
	want := map[string]Kind{
		"gitlab_create_project": Ordinary, "gitlab_get_project": Ordinary, "gitlab_list_branches": Ordinary,
		"gitlab_create_branch": Ordinary, "gitlab_commit_files": Ordinary, "gitlab_create_merge_request": Ordinary,
		"gitlab_get_pipeline_status": Ordinary,
		"cloudrun_deploy_preview":    Ordinary, "cloudrun_get_service": Ordinary, "cloudrun_list_revisions": Ordinary,
		"cloudrun_read_logs": Ordinary, "cloudrun_delete_preview_service": DataDeletion, "cloudrun_set_iam_invoker": IAMChange,
		"artifactregistry_list_images": Ordinary, "artifactregistry_list_tags": Ordinary, "artifactregistry_get_digest": Ordinary,
		"bigquery_list_datasets": Ordinary, "bigquery_list_tables": Ordinary, "bigquery_get_schema": Ordinary,
		"bigquery_dry_run_query": Ordinary, "bigquery_run_query": Ordinary, "bigquery_create_dataset": NewSpend,
		"bigquery_delete_table":       DataDeletion,
		"workbench_request_promotion": ProductionDeploy, "workbench_show": Ordinary, "workbench_register_app": Ordinary,
		"workbench_record_deployment": Ordinary, "workbench_get_app": Ordinary,
	}
	for tool, kind := range want {
		if got := p.Classify(tool, nil); got != kind {
			t.Errorf("%s: got %s, want %s", tool, got, kind)
		}
	}
	if p.Classify("cloudrun_deploy_preview", map[string]any{"allow_create": true}) != NewSpend {
		t.Error("creating a preview service is new spend")
	}
}
