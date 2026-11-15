// Package policy is the machine-readable always-ask classification of the
// Workbench tool servers' tools. Workbench's approval policy can import it
// (or mirror its patterns); the tool-server tests assert that every
// registered tool is listed here and that names, classes and MCP annotations
// agree.
package policy

import (
	"path"
	"slices"
)

// Class is an always-ask category.
type Class string

const (
	// DataDeletion: irreversibly deletes data or a running service.
	DataDeletion Class = "data_deletion"
	// IAMChange: changes who can access something.
	IAMChange Class = "iam_change"
	// NewSpend: creates a billable resource.
	NewSpend Class = "new_spend"
)

// Patterns maps tool-name glob patterns (path.Match syntax) to classes.
var Patterns = []struct {
	Pattern string
	Class   Class
}{
	{"*_delete_*", DataDeletion},
	{"*_iam_*", IAMChange},
	{"*_create_dataset", NewSpend},
}

// ConditionalSpend lists tools that are NewSpend only when a boolean
// argument is true. cloudrun_deploy_preview creates a new (billable) preview
// service only with allow_create=true; without it the server refuses to create.
var ConditionalSpend = map[string]string{
	"cloudrun_deploy_preview": "allow_create",
}

// Classify returns the always-ask classes of a tool call. args may be nil
// (then conditional classes are included, i.e. the conservative answer).
func Classify(tool string, args map[string]any) []Class {
	var out []Class
	for _, p := range Patterns {
		if ok, _ := path.Match(p.Pattern, tool); ok && !slices.Contains(out, p.Class) {
			out = append(out, p.Class)
		}
	}
	if arg, ok := ConditionalSpend[tool]; ok && (args == nil || args[arg] == true) {
		out = append(out, NewSpend)
	}
	return out
}

// AlwaysAsk reports whether a call needs human approval.
func AlwaysAsk(tool string, args map[string]any) bool { return len(Classify(tool, args)) > 0 }

// Tools is the full catalog: server -> tool names.
var Tools = map[string][]string{
	"gitlab-mcp": {
		"gitlab_create_project", "gitlab_get_project", "gitlab_list_branches", "gitlab_list_files",
		"gitlab_get_file", "gitlab_create_branch", "gitlab_commit_files", "gitlab_create_merge_request",
		"gitlab_get_pipeline_status",
	},
	"build-mcp": {
		"build_start_image", "build_get_status",
	},
	"cloudrun-mcp": {
		"cloudrun_deploy_preview", "cloudrun_get_service", "cloudrun_list_revisions", "cloudrun_read_logs",
		"cloudrun_delete_preview_service", "cloudrun_set_iam_invoker",
	},
	"artifactregistry-mcp": {
		"artifactregistry_list_images", "artifactregistry_list_tags", "artifactregistry_get_digest",
	},
	"bigquery-mcp": {
		"bigquery_list_datasets", "bigquery_list_tables", "bigquery_get_schema", "bigquery_dry_run_query",
		"bigquery_run_query", "bigquery_create_dataset", "bigquery_delete_table",
	},
}
