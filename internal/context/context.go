// Package context compiles organization state into the minimal context needed
// by an individual agent role.
package context

import (
	"fmt"
	"sort"
	"strings"

	"github.com/agent-consensus/ac/internal/model"
	"github.com/agent-consensus/ac/internal/store"
)

func Build(state store.State, role, agent string) (model.ContextDocument, error) {
	config, err := state.LoadConfig()
	if err != nil {
		return model.ContextDocument{}, err
	}
	decisions, err := state.ListDecisions()
	if err != nil {
		return model.ContextDocument{}, err
	}
	localDecisions, err := state.ListEffectiveLocalDecisions()
	if err != nil {
		return model.ContextDocument{}, err
	}
	decisions = append(decisions, localDecisions...)
	rules, err := state.ListRules()
	if err != nil {
		return model.ContextDocument{}, err
	}
	events, err := state.ListEvents()
	if err != nil {
		return model.ContextDocument{}, err
	}

	return BuildDocument(config.Organization, config.Repository, decisions, rules, events, role, agent)
}

// BuildDocument compiles consensus that may come from either a local state
// directory or an aggregated agc server organization. Events are the
// repository-level case records; callers pass an empty slice when compiling
// organization-wide context not bound to a repository.
func BuildDocument(organization, repository string, decisions []model.Decision, rules []model.Policy, events []model.Event, role, agent string) (model.ContextDocument, error) {
	document := model.ContextDocument{
		Organization: strings.TrimSpace(organization),
		Repository:   strings.TrimSpace(repository),
		Role:         strings.TrimSpace(role),
		Agent:        strings.TrimSpace(agent),
		Decisions:    make([]model.ContextItem, 0),
		Rules:        make([]model.ContextItem, 0),
		Events:       make([]model.ContextItem, 0),
	}
	for _, decision := range decisions {
		if strings.EqualFold(decision.Status, "active") && model.ScopeMatches(decision.Scope, document.Role) {
			document.Decisions = append(document.Decisions, itemFromDecision(decision))
		}
	}
	for _, policy := range rules {
		if strings.EqualFold(policy.Status, "active") && model.ScopeMatches(policy.Scope, document.Role) {
			document.Rules = append(document.Rules, itemFromPolicy(policy))
		}
	}
	for _, event := range events {
		if model.ScopeMatches(event.Scope, document.Role) {
			document.Events = append(document.Events, itemFromEvent(event))
		}
	}
	sort.Slice(document.Decisions, func(i, j int) bool { return document.Decisions[i].ID < document.Decisions[j].ID })
	sort.Slice(document.Rules, func(i, j int) bool { return document.Rules[i].ID < document.Rules[j].ID })
	sort.Slice(document.Events, func(i, j int) bool { return document.Events[i].ID < document.Events[j].ID })
	contextHash, err := model.ContextHash(document)
	if err != nil {
		return model.ContextDocument{}, err
	}
	document.ContextHash = contextHash
	return document, nil
}

func itemFromDecision(decision model.Decision) model.ContextItem {
	return model.ContextItem{ID: decision.ID, Title: decision.Title, Statement: decision.Statement, Scope: decision.Scope}
}

func itemFromPolicy(policy model.Policy) model.ContextItem {
	return model.ContextItem{ID: policy.ID, Title: policy.Title, Statement: policy.Statement, Scope: policy.Scope}
}

func itemFromEvent(event model.Event) model.ContextItem {
	return model.ContextItem{ID: event.ID, Title: event.Title, Statement: event.Statement, Scope: event.Scope}
}

// Markdown renders an intentionally compact system-context fragment.
func Markdown(document model.ContextDocument) string {
	var builder strings.Builder
	builder.WriteString("# Agent Context\n\n")
	fmt.Fprintf(&builder, "Organization: %s\n", document.Organization)
	fmt.Fprintf(&builder, "Repository: %s\n", document.Repository)
	if document.Role == "" {
		builder.WriteString("Current Role: all\n")
	} else {
		fmt.Fprintf(&builder, "Current Role: %s\n", document.Role)
	}
	if document.Agent != "" {
		fmt.Fprintf(&builder, "Agent: %s\n", document.Agent)
	}
	fmt.Fprintf(&builder, "Context Hash: %s\n\n", document.ContextHash)

	builder.WriteString("## Active Decisions\n\n")
	writeItems(&builder, document.Decisions)
	builder.WriteString("\n## Organization Rules\n\n")
	writeItems(&builder, document.Rules)
	builder.WriteString("\n## Known Cases\n\n")
	writeItems(&builder, document.Events)
	return builder.String()
}

func writeItems(builder *strings.Builder, items []model.ContextItem) {
	if len(items) == 0 {
		builder.WriteString("None.\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(builder, "- **%s — %s**\n  %s\n", item.ID, item.Title, item.Statement)
	}
}
