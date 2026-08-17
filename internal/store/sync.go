package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/agent-consensus/ac/internal/model"
)

// SnapshotConflict keeps local repository state safe when a pull sees two
// different versions of the same item. Pull never silently overwrites it.
type SnapshotConflict struct {
	UID    string
	ID     string
	Reason string
}

type SnapshotApplyResult struct {
	Applied            int
	Unchanged          int
	ResolvedProposals  int
	ResolvedPromotions int
	Conflicts          []SnapshotConflict
}

// ApplyRemoteSnapshot merges the organization Rules and the requesting
// repository's Decisions and Events. Decisions and events stay in their local
// repository directories; only Rules are organization-wide consensus.
func (s State) ApplyRemoteSnapshot(remoteDecisions []model.Decision, remoteRules []model.Policy, remoteEvents []model.Event, resolvedProposals []model.Proposal, resolvedPromotions []model.Promotion) (SnapshotApplyResult, error) {
	result := SnapshotApplyResult{Conflicts: make([]SnapshotConflict, 0)}
	if err := s.EnsureLayout(); err != nil {
		return result, err
	}
	localDecisions, err := s.ListDecisions()
	if err != nil {
		return result, err
	}
	localRules, err := s.ListRules()
	if err != nil {
		return result, err
	}
	localEvents, err := s.ListEvents()
	if err != nil {
		return result, err
	}

	mergeDecisions, err := s.mergeDecisions(localDecisions, remoteDecisions, &result)
	if err != nil {
		return result, err
	}
	mergeRules, err := s.mergeRules(localRules, remoteRules, &result)
	if err != nil {
		return result, err
	}
	mergeEvents, err := s.mergeEvents(localEvents, remoteEvents, &result)
	if err != nil {
		return result, err
	}
	if len(result.Conflicts) > 0 {
		sort.Slice(result.Conflicts, func(i, j int) bool { return result.Conflicts[i].ID < result.Conflicts[j].ID })
		return result, nil
	}
	for _, decision := range mergeDecisions {
		if err := writeMerged(s.DecisionsDir(), decision.ID, decision); err != nil {
			return result, fmt.Errorf("write repository decision %s: %w", decision.ID, err)
		}
		result.Applied++
	}
	for _, rule := range mergeRules {
		if err := writeMerged(s.RulesDir(), rule.ID, rule); err != nil {
			return result, fmt.Errorf("write organization rule %s: %w", rule.ID, err)
		}
		result.Applied++
	}
	for _, event := range mergeEvents {
		if err := writeMerged(s.EventsDir(), event.ID, event); err != nil {
			return result, fmt.Errorf("write repository event %s: %w", event.ID, err)
		}
		result.Applied++
	}
	resolvedProposalsCount, err := s.ApplyResolvedProposals(resolvedProposals)
	if err != nil {
		return result, err
	}
	result.ResolvedProposals = resolvedProposalsCount
	resolved, err := s.ApplyResolvedPromotions(resolvedPromotions)
	if err != nil {
		return result, err
	}
	result.ResolvedPromotions = resolved
	return result, nil
}

func (s State) mergeDecisions(local, remote []model.Decision, result *SnapshotApplyResult) ([]model.Decision, error) {
	byUID := make(map[string]model.Decision, len(local))
	for _, item := range local {
		byUID[item.UID] = item
	}
	plans := make([]model.Decision, 0, len(remote))
	for _, item := range remote {
		item.Scope = model.NormalizeScope(item.Scope)
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("invalid server decision %s: %w", item.ID, err)
		}
		if err := validateDecisionHash(item); err != nil {
			return nil, err
		}
		previous, exists := byUID[item.UID]
		if !exists {
			plans = append(plans, item)
			continue
		}
		switch {
		case previous.Revision < item.Revision:
			plans = append(plans, item)
		case previous.Revision == item.Revision && previous.ContentHash == item.ContentHash:
			result.Unchanged++
		case previous.Revision == item.Revision:
			result.Conflicts = append(result.Conflicts, SnapshotConflict{UID: item.UID, ID: item.ID, Reason: "same revision has different content"})
		default:
			result.Conflicts = append(result.Conflicts, SnapshotConflict{UID: item.UID, ID: item.ID, Reason: "local revision is newer than server"})
		}
	}
	return plans, nil
}

func (s State) mergeRules(local, remote []model.Policy, result *SnapshotApplyResult) ([]model.Policy, error) {
	byUID := make(map[string]model.Policy, len(local))
	for _, item := range local {
		byUID[item.UID] = item
	}
	plans := make([]model.Policy, 0, len(remote))
	for _, item := range remote {
		item.Scope = model.NormalizeScope(item.Scope)
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("invalid server rule %s: %w", item.ID, err)
		}
		if err := validateRuleHash(item); err != nil {
			return nil, err
		}
		previous, exists := byUID[item.UID]
		if !exists || item.Revision > previous.Revision {
			plans = append(plans, item)
		} else if item.Revision == previous.Revision && item.ContentHash == previous.ContentHash {
			result.Unchanged++
		} else if item.Revision == previous.Revision {
			result.Conflicts = append(result.Conflicts, SnapshotConflict{UID: item.UID, ID: item.ID, Reason: "same revision has different content"})
		} else {
			result.Conflicts = append(result.Conflicts, SnapshotConflict{UID: item.UID, ID: item.ID, Reason: "local revision is newer than server"})
		}
	}
	return plans, nil
}

func (s State) mergeEvents(local, remote []model.Event, result *SnapshotApplyResult) ([]model.Event, error) {
	byUID := make(map[string]model.Event, len(local))
	for _, item := range local {
		byUID[item.UID] = item
	}
	plans := make([]model.Event, 0, len(remote))
	for _, item := range remote {
		item.Scope = model.NormalizeScope(item.Scope)
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("invalid server event %s: %w", item.ID, err)
		}
		if err := validateEventHash(item); err != nil {
			return nil, err
		}
		previous, exists := byUID[item.UID]
		if !exists {
			plans = append(plans, item)
		} else if previous.ContentHash == item.ContentHash {
			result.Unchanged++
		} else {
			result.Conflicts = append(result.Conflicts, SnapshotConflict{UID: item.UID, ID: item.ID, Reason: "events are immutable and content differs"})
		}
	}
	return plans, nil
}

func writeMerged(directory, id string, value any) error {
	path := filepath.Join(directory, id+".yaml")
	if _, err := os.Stat(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeYAML(path, value)
}

func validateDecisionHash(decision model.Decision) error {
	expected := decision
	if err := expected.RefreshContentHash(); err != nil {
		return err
	}
	if expected.ContentHash != decision.ContentHash {
		return fmt.Errorf("invalid server decision %s: content_hash does not match content", decision.ID)
	}
	return nil
}

func validateRuleHash(rule model.Policy) error {
	expected := rule
	if err := expected.RefreshContentHash(); err != nil {
		return err
	}
	if expected.ContentHash != rule.ContentHash {
		return fmt.Errorf("invalid server rule %s: content_hash does not match content", rule.ID)
	}
	return nil
}
