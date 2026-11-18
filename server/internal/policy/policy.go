// Package policy classifies pending tool calls into action kinds and says who
// may decide them. It holds the rules; it does not store or send anything.
package policy

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// Kind is an action kind. Every kind except Ordinary always asks: its answers
// are never remembered and never applied automatically.
type Kind string

const (
	Ordinary         Kind = "ordinary"
	ProductionDeploy Kind = "production_deploy"
	IAMChange        Kind = "iam_change"
	DataDeletion     Kind = "data_deletion"
	NewSpend         Kind = "new_spend"
)

// severity orders kinds from least to most restrictive. An approval covering
// several calls takes its most restrictive call's kind.
var severity = map[Kind]int{Ordinary: 0, NewSpend: 1, DataDeletion: 2, IAMChange: 3, ProductionDeploy: 4}

// AlwaysAsk reports whether answers for this kind may never be remembered.
func (k Kind) AlwaysAsk() bool { return k != Ordinary }

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { _, ok := severity[k]; return ok }

// Condition narrows a rule to calls whose argument has a given value.
type Condition struct {
	Arg    string   `json:"arg" yaml:"arg"`
	Equals []string `json:"equals" yaml:"equals"`
}

// Rule maps tool names to a kind.
type Rule struct {
	// Tools are glob patterns (path.Match syntax) on the tool name.
	Tools []string `json:"tools" yaml:"tools"`
	// When, if set, must all hold.
	When []Condition `json:"when,omitempty" yaml:"when,omitempty"`
}

// KindPolicy says how one always-ask kind is recognised and decided.
type KindPolicy struct {
	Kind        Kind   `json:"kind" yaml:"kind"`
	Description string `json:"description" yaml:"description"`
	Rules       []Rule `json:"rules" yaml:"rules"`
	// ApproverGroups may decide. Empty means only the requester decides.
	ApproverGroups []string `json:"approverGroups" yaml:"approverGroups"`
	// RequesterMayDecide lets the requester decide their own call.
	RequesterMayDecide bool `json:"requesterMayDecide" yaml:"requesterMayDecide"`
}

// Policy is the whole approval policy. It is authored in Git and reviewed.
type Policy struct {
	Kinds []KindPolicy `json:"kinds" yaml:"kinds"`
}

// Default is the strict, narrow starting policy: four always-ask kinds and
// one approver group. Production deploys and IAM changes need someone other
// than the requester.
func Default(approverGroup string) Policy {
	return Policy{Kinds: []KindPolicy{
		{
			Kind:        ProductionDeploy,
			Description: "Putting an app into production",
			Rules: []Rule{
				{Tools: []string{"workbench_request_promotion", "*promote*"}},
				{Tools: []string{"*deploy*"}, When: []Condition{{Arg: "environment", Equals: []string{"production", "prod"}}}},
			},
			ApproverGroups:     []string{approverGroup},
			RequesterMayDecide: false,
		},
		{
			Kind:               IAMChange,
			Description:        "Changing who or what can access a cloud resource",
			Rules:              []Rule{{Tools: []string{"*iam*", "*_grant_*", "*_revoke_*", "*set_policy*"}}},
			ApproverGroups:     []string{approverGroup},
			RequesterMayDecide: false,
		},
		{
			Kind:               DataDeletion,
			Description:        "Deleting data or a resource that holds it",
			Rules:              []Rule{{Tools: []string{"*_delete_*", "*_drop_*", "*_truncate_*", "delete_*", "drop_*"}}},
			ApproverGroups:     []string{approverGroup},
			RequesterMayDecide: true,
		},
		{
			Kind:        NewSpend,
			Description: "Creating something that costs money every month",
			Rules: []Rule{
				{Tools: []string{"*_create_dataset", "*_create_instance", "*_create_cluster", "*_create_bucket"}},
				{Tools: []string{"cloudrun_deploy_preview"}, When: []Condition{{Arg: "allow_create", Equals: []string{"true"}}}},
			},
			ApproverGroups:     []string{approverGroup},
			RequesterMayDecide: true,
		},
	}}
}

// Validate checks that the policy is well formed.
func (p Policy) Validate() error {
	seen := map[Kind]bool{}
	for _, k := range p.Kinds {
		if !k.Kind.Valid() || k.Kind == Ordinary {
			return fmt.Errorf("policy: %q is not an always-ask kind", k.Kind)
		}
		if seen[k.Kind] {
			return fmt.Errorf("policy: kind %s listed twice", k.Kind)
		}
		seen[k.Kind] = true
		if len(k.Rules) == 0 {
			return fmt.Errorf("policy: kind %s has no rules", k.Kind)
		}
		if !k.RequesterMayDecide && len(k.ApproverGroups) == 0 {
			return fmt.Errorf("policy: kind %s bars the requester but names no approver group, so nobody could decide it", k.Kind)
		}
		for _, r := range k.Rules {
			if len(r.Tools) == 0 {
				return fmt.Errorf("policy: kind %s has a rule with no tools", k.Kind)
			}
			for _, pat := range r.Tools {
				if _, err := path.Match(pat, ""); err != nil {
					return fmt.Errorf("policy: kind %s: bad pattern %q: %w", k.Kind, pat, err)
				}
			}
		}
	}
	return nil
}

// Classify returns the most restrictive kind any rule assigns to the call.
func (p Policy) Classify(tool string, args map[string]any) Kind {
	name := normalise(tool)
	best := Ordinary
	for _, k := range p.Kinds {
		if severity[k.Kind] <= severity[best] {
			continue
		}
		if slices.ContainsFunc(k.Rules, func(r Rule) bool { return r.matches(name, args) }) {
			best = k.Kind
		}
	}
	return best
}

// Most returns the most restrictive of the kinds.
func Most(kinds ...Kind) Kind {
	best := Ordinary
	for _, k := range kinds {
		if severity[k] > severity[best] {
			best = k
		}
	}
	return best
}

// For returns the policy for a kind. Ordinary calls are decided by the
// requester alone.
func (p Policy) For(kind Kind) KindPolicy {
	for _, k := range p.Kinds {
		if k.Kind == kind {
			return k
		}
	}
	return KindPolicy{Kind: Ordinary, Description: "An ordinary tool call", RequesterMayDecide: true}
}

func (r Rule) matches(name string, args map[string]any) bool {
	if !slices.ContainsFunc(r.Tools, func(pat string) bool { ok, _ := path.Match(strings.ToLower(pat), name); return ok }) {
		return false
	}
	for _, c := range r.When {
		v, ok := args[c.Arg]
		if !ok {
			return false
		}
		s := strings.ToLower(fmt.Sprint(v))
		if !slices.ContainsFunc(c.Equals, func(e string) bool { return strings.ToLower(e) == s }) {
			return false
		}
	}
	return true
}

// normalise strips an MCP server prefix some runtimes add ("server__tool" or
// "server/tool") so that rules match the tool's own name, and lowercases it.
func normalise(tool string) string {
	tool = strings.ToLower(tool)
	if i := strings.LastIndex(tool, "__"); i >= 0 {
		tool = tool[i+2:]
	}
	if i := strings.LastIndex(tool, "/"); i >= 0 {
		tool = tool[i+1:]
	}
	return tool
}

// Decider is someone who might decide an approval.
type Decider struct {
	ID     string
	Groups []string
}

// Routing is who may decide one approval.
type Routing struct {
	Kind               Kind
	RequesterID        string
	ApproverGroups     []string
	ApproverIDs        []string
	RequesterMayDecide bool
}

// Route builds the routing for an approval of the given kind.
func (p Policy) Route(kind Kind, requesterID string) Routing {
	kp := p.For(kind)
	return Routing{
		Kind:               kind,
		RequesterID:        requesterID,
		ApproverGroups:     slices.Clone(kp.ApproverGroups),
		RequesterMayDecide: kp.RequesterMayDecide,
	}
}

// MayDecide is the one place the "who may decide" rule lives. A requester may
// decide only when the kind allows it; anyone else must be in an approver
// group or have been named on the approval.
func (r Routing) MayDecide(d Decider) bool {
	if d.ID == "" {
		return false
	}
	if d.ID == r.RequesterID {
		return r.RequesterMayDecide
	}
	if slices.Contains(r.ApproverIDs, d.ID) {
		return true
	}
	return slices.ContainsFunc(d.Groups, func(g string) bool { return slices.Contains(r.ApproverGroups, g) })
}
