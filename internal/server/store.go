// Package server implements the durable organization/repository boundary.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/agent-consensus/ac/internal/api"
	agentcontext "github.com/agent-consensus/ac/internal/context"
	"github.com/agent-consensus/ac/internal/model"
)

var (
	ErrNotFound      = errors.New("organization or consensus item was not found")
	ErrAlreadyExists = errors.New("organization already exists")
	ErrConflict      = errors.New("consensus conflict")
)

// The field migration below keeps v1 state readable, so this value remains 1.
const dataSchemaVersion = 1

var serverRuleIDPattern = regexp.MustCompile(`^R-(\d+)$`)
var serverDecisionIDPattern = regexp.MustCompile(`^D-(\d+)$`)
var serverProposalIDPattern = regexp.MustCompile(`^P-(\d+)$`)
var serverPromotionIDPattern = regexp.MustCompile(`^M-(\d+)$`)
var serverContextRecordIDPattern = regexp.MustCompile(`^CR-(\d+)$`)

type FileStore struct {
	path string
	mu   sync.RWMutex
	data persistedState
}

type persistedState struct {
	SchemaVersion int                           `json:"schema_version"`
	Organizations map[string]*organizationState `json:"organizations"`
}

// organizationState intentionally separates organization Rules from each
// repository's Decisions and Events. Legacy fields are read once and migrated
// in normalizePersistedState; new writes omit them.
type organizationState struct {
	Slug                 string                               `json:"slug"`
	Name                 string                               `json:"name"`
	CreatedAt            time.Time                            `json:"created_at"`
	UpdatedAt            time.Time                            `json:"updated_at"`
	Repositories         map[string]api.RepositorySummary     `json:"repositories"`
	RepositoryDecisions  map[string]map[string]model.Decision `json:"repository_decisions"`
	RepositoryEvents     map[string]map[string]model.Event    `json:"repository_events"`
	Rules                map[string]model.Policy              `json:"rules"`
	Proposals            map[string]model.Proposal            `json:"proposals"`
	Promotions           map[string]model.Promotion           `json:"promotions"`
	ContextRecords       []api.ContextRecord                  `json:"context_records"`
	LegacyDecisions      map[string]model.Decision            `json:"decisions,omitempty"`
	LegacyDecisionSource map[string]string                    `json:"decision_sources,omitempty"`
	LegacyPolicies       map[string]model.Policy              `json:"policies,omitempty"`
}

func OpenFileStore(path string) (*FileStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("server data path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create server data directory: %w", err)
	}
	data := newPersistedState()
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		if err := json.NewDecoder(file).Decode(&data); err != nil {
			return nil, fmt.Errorf("read server data: %w", err)
		}
		if data.SchemaVersion != dataSchemaVersion {
			return nil, fmt.Errorf("unsupported server data schema version %d", data.SchemaVersion)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("open server data: %w", err)
	}
	normalizePersistedState(&data)
	return &FileStore{path: path, data: data}, nil
}

func (s *FileStore) ListOrganizations() []api.OrganizationSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]api.OrganizationSummary, 0, len(s.data.Organizations))
	for _, organization := range s.data.Organizations {
		result = append(result, summaryFromOrganization(organization))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *FileStore) CreateOrganization(name string) (api.OrganizationSummary, error) {
	var summary api.OrganizationSummary
	err := s.mutate(func(data *persistedState) error {
		slug, err := organizationSlug(name)
		if err != nil {
			return err
		}
		if _, exists := data.Organizations[slug]; exists {
			return ErrAlreadyExists
		}
		organization := newOrganization(slug, strings.TrimSpace(name), time.Now().UTC())
		data.Organizations[slug] = organization
		summary = summaryFromOrganization(organization)
		return nil
	})
	return summary, err
}

func (s *FileStore) GetOrganization(slug string) (api.OrganizationDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	organization, err := findOrganization(s.data, slug)
	if err != nil {
		return api.OrganizationDetail{}, err
	}
	return detailFromOrganization(organization), nil
}

// Sync stores one repository's Decisions and Events. It intentionally cannot
// write Rules: organization consensus is available only through promotion.
func (s *FileStore) Sync(slug string, request api.SyncRequest) (api.SyncResponse, error) {
	var response api.SyncResponse
	err := s.mutate(func(data *persistedState) error {
		pathSlug, err := organizationSlug(slug)
		if err != nil {
			return err
		}
		if request.Organization != "" {
			requestSlug, err := organizationSlug(request.Organization)
			if err != nil {
				return err
			}
			if requestSlug != pathSlug {
				return fmt.Errorf("request organization does not match URL organization")
			}
		}
		repository := strings.TrimSpace(request.Repository)
		if repository == "" {
			return fmt.Errorf("repository is required")
		}
		now := time.Now().UTC()
		organization := data.Organizations[pathSlug]
		if organization == nil {
			organization = newOrganization(pathSlug, firstNonBlank(request.Organization, slug), now)
			data.Organizations[pathSlug] = organization
		}
		decisions := organization.RepositoryDecisions[repository]
		if decisions == nil {
			decisions = make(map[string]model.Decision)
			organization.RepositoryDecisions[repository] = decisions
		}
		events := organization.RepositoryEvents[repository]
		if events == nil {
			events = make(map[string]model.Event)
			organization.RepositoryEvents[repository] = events
		}
		response.Conflicts = make([]api.SyncConflict, 0)
		for _, incoming := range request.Decisions {
			incoming.Scope = model.NormalizeScope(incoming.Scope)
			if err := incoming.Validate(); err != nil {
				return fmt.Errorf("invalid repository decision %s: %w", incoming.ID, err)
			}
			if err := verifyDecisionHash(incoming); err != nil {
				return err
			}
			existing, exists := decisions[incoming.UID]
			switch {
			case !exists || incoming.Revision > existing.Revision:
				decisions[incoming.UID] = incoming
				response.Applied++
			case incoming.Revision == existing.Revision && incoming.ContentHash == existing.ContentHash:
				response.Unchanged++
			case incoming.Revision == existing.Revision:
				response.Conflicts = append(response.Conflicts, api.SyncConflict{UID: incoming.UID, ID: incoming.ID, Reason: "same revision has different content"})
			default:
				response.Unchanged++
			}
		}
		for _, incoming := range request.Events {
			incoming.Scope = model.NormalizeScope(incoming.Scope)
			if err := incoming.Validate(); err != nil {
				return fmt.Errorf("invalid repository event %s: %w", incoming.ID, err)
			}
			if err := verifyEventHash(incoming); err != nil {
				return err
			}
			existing, exists := events[incoming.UID]
			switch {
			case !exists:
				events[incoming.UID] = incoming
				response.Applied++
			case existing.ContentHash == incoming.ContentHash:
				response.Unchanged++
			default:
				response.Conflicts = append(response.Conflicts, api.SyncConflict{UID: incoming.UID, ID: incoming.ID, Reason: "events are immutable and content differs"})
			}
		}
		updateRepositorySummary(organization, repository, request.Source, request.RepositoryURL, now)
		organization.UpdatedAt = now
		response.Organization = summaryFromOrganization(organization)
		response.SyncedAt = now
		return nil
	})
	return response, err
}

// DeleteRepositoryDecision removes one repository-scoped decision. The
// identifier accepts either the decision UID or its human ID (D-###).
func (s *FileStore) DeleteRepositoryDecision(slug, repository, identifier string) error {
	return s.mutate(func(data *persistedState) error {
		organization, err := findOrganization(*data, slug)
		if err != nil {
			return err
		}
		repository = strings.TrimSpace(repository)
		identifier = strings.TrimSpace(identifier)
		decisions := organization.RepositoryDecisions[repository]
		uid := ""
		for candidate, decision := range decisions {
			if candidate == identifier || decision.ID == identifier {
				uid = candidate
				break
			}
		}
		if uid == "" {
			return ErrNotFound
		}
		delete(decisions, uid)
		now := time.Now().UTC()
		updateRepositorySummary(organization, repository, "", "", now)
		organization.UpdatedAt = now
		return nil
	})
}

// SubmitProposal stores a temporary local decision for repository-level
// review. It deliberately does not create a repository Decision yet.
func (s *FileStore) SubmitProposal(slug string, request api.SubmitProposalRequest) (model.Proposal, error) {
	var result model.Proposal
	err := s.mutate(func(data *persistedState) error {
		organization, err := findOrganization(*data, slug)
		if err != nil {
			return err
		}
		repository := strings.TrimSpace(request.Repository)
		if repository == "" {
			return fmt.Errorf("repository is required")
		}
		proposal := request.Proposal
		proposal.Scope = model.NormalizeScope(proposal.Scope)
		if proposal.Status != "local" && proposal.Status != "submitted" {
			return fmt.Errorf("proposal %s must be local or submitted before review", proposal.ID)
		}
		if err := proposal.Validate(); err != nil {
			return fmt.Errorf("invalid proposal %s: %w", proposal.ID, err)
		}
		if err := verifyProposalHash(proposal); err != nil {
			return err
		}
		if existing, exists := organization.Proposals[proposal.UID]; exists {
			if existing.ContentHash != proposal.ContentHash || existing.Repository != repository {
				return fmt.Errorf("%w: proposal %s has different content", ErrConflict, existing.ID)
			}
			result = existing
			return nil
		}
		now := time.Now().UTC()
		proposal.ID = nextProposalID(organization)
		proposal.Repository = repository
		proposal.Status = "pending"
		proposal.RemoteID, proposal.DecisionID, proposal.ReviewNote = "", "", ""
		proposal.SubmittedAt, proposal.ReviewedAt, proposal.UpdatedAt = now, time.Time{}, now
		organization.Proposals[proposal.UID] = proposal
		updateRepositorySummary(organization, repository, request.Source, "", now)
		organization.UpdatedAt = now
		result = proposal
		return nil
	})
	return result, err
}

// ReviewProposal materializes an approved candidate as a Decision in only the
// source repository. A rejected candidate never becomes remotely shared.
func (s *FileStore) ReviewProposal(slug, identifier string, request api.ReviewProposalRequest) (model.Proposal, error) {
	var result model.Proposal
	err := s.mutate(func(data *persistedState) error {
		organization, err := findOrganization(*data, slug)
		if err != nil {
			return err
		}
		uid, proposal, found := findProposal(organization, identifier)
		if !found {
			return ErrNotFound
		}
		if proposal.Status != "pending" {
			return fmt.Errorf("%w: proposal %s is already %s", ErrConflict, proposal.ID, proposal.Status)
		}
		status := strings.ToLower(strings.TrimSpace(request.Status))
		if status != "approved" && status != "rejected" {
			return fmt.Errorf("proposal review status must be approved or rejected")
		}
		if strings.TrimSpace(request.Title) != "" {
			proposal.Title = strings.TrimSpace(request.Title)
		}
		if strings.TrimSpace(request.Statement) != "" {
			proposal.Statement = strings.TrimSpace(request.Statement)
		}
		if len(request.Scope) > 0 {
			proposal.Scope = model.NormalizeScope(request.Scope)
		}
		if strings.TrimSpace(request.Owner) != "" {
			proposal.Owner = strings.TrimSpace(request.Owner)
		}
		if err := proposal.Validate(); err != nil {
			return fmt.Errorf("reviewed proposal %s is invalid: %w", proposal.ID, err)
		}
		if err := proposal.RefreshContentHash(); err != nil {
			return err
		}
		now := time.Now().UTC()
		proposal.Status, proposal.ReviewNote, proposal.ReviewedAt, proposal.UpdatedAt = status, strings.TrimSpace(request.ReviewNote), now, now
		if status == "approved" {
			decisions := organization.RepositoryDecisions[proposal.Repository]
			if decisions == nil {
				decisions = make(map[string]model.Decision)
				organization.RepositoryDecisions[proposal.Repository] = decisions
			}
			decision, err := proposal.AsLocalDecision()
			if err != nil {
				return err
			}
			decision.ID, decision.Status, decision.UpdatedAt = nextRepositoryDecisionID(decisions), "active", now
			if err := decision.RefreshContentHash(); err != nil {
				return err
			}
			decisions[decision.UID] = decision
			proposal.DecisionID = decision.ID
		}
		organization.Proposals[uid] = proposal
		updateRepositorySummary(organization, proposal.Repository, "", "", now)
		organization.UpdatedAt = now
		result = proposal
		return nil
	})
	return result, err
}

func (s *FileStore) SubmitPromotion(slug string, request api.SubmitPromotionRequest) (model.Promotion, error) {
	var result model.Promotion
	err := s.mutate(func(data *persistedState) error {
		organization, err := findOrganization(*data, slug)
		if err != nil {
			return err
		}
		repository := strings.TrimSpace(request.Repository)
		if repository == "" {
			return fmt.Errorf("repository is required")
		}
		promotion := request.Promotion
		if promotion.Status != "local" && promotion.Status != "submitted" {
			return fmt.Errorf("promotion %s must be local or submitted before review", promotion.ID)
		}
		if err := promotion.Validate(); err != nil {
			return fmt.Errorf("invalid promotion %s: %w", promotion.ID, err)
		}
		decision, exists := organization.RepositoryDecisions[repository][promotion.DecisionUID]
		if !exists || decision.ID != promotion.DecisionID || decision.ContentHash != promotion.DecisionHash {
			return fmt.Errorf("%w: decision %s must be synced unchanged before promotion", ErrConflict, promotion.DecisionID)
		}
		if existing, exists := organization.Promotions[promotion.UID]; exists {
			if existing.DecisionHash != promotion.DecisionHash || existing.Repository != repository {
				return fmt.Errorf("%w: promotion %s has different content", ErrConflict, existing.ID)
			}
			result = existing
			return nil
		}
		// Clients without local state (e.g. the dashboard) generate a fresh UID
		// per request; guard by decision so repeat submissions cannot pile up
		// duplicate pending promotions for the same decision.
		for _, existing := range organization.Promotions {
			if existing.Repository == repository && existing.DecisionUID == promotion.DecisionUID && existing.Status == "pending" {
				return fmt.Errorf("%w: decision %s already has pending promotion %s", ErrConflict, existing.DecisionID, existing.ID)
			}
		}
		now := time.Now().UTC()
		promotion.ID = nextPromotionID(organization)
		promotion.Repository = repository
		promotion.Status = "pending"
		promotion.RemoteID = ""
		promotion.RuleID = ""
		promotion.ReviewNote = ""
		promotion.SubmittedAt = now
		promotion.ReviewedAt = time.Time{}
		promotion.UpdatedAt = now
		organization.Promotions[promotion.UID] = promotion
		updateRepositorySummary(organization, repository, request.Source, "", now)
		organization.UpdatedAt = now
		result = promotion
		return nil
	})
	return result, err
}

// SubmitEvent is a convenience endpoint for integrations. It follows the
// exact same repository-scoped validation and idempotency rules as sync.
func (s *FileStore) SubmitEvent(slug string, request api.SubmitEventRequest) (model.Event, error) {
	response, err := s.Sync(slug, api.SyncRequest{
		Repository: request.Repository,
		Source:     request.Source,
		SentAt:     time.Now().UTC(),
		Events:     []model.Event{request.Event},
	})
	if err != nil {
		return model.Event{}, err
	}
	if len(response.Conflicts) > 0 {
		return model.Event{}, fmt.Errorf("%w: event %s: %s", ErrConflict, request.Event.ID, response.Conflicts[0].Reason)
	}
	return request.Event, nil
}

// ReviewPromotion is the only route which may create an organization Rule.
func (s *FileStore) ReviewPromotion(slug, identifier string, request api.ReviewPromotionRequest) (model.Promotion, error) {
	var result model.Promotion
	err := s.mutate(func(data *persistedState) error {
		organization, err := findOrganization(*data, slug)
		if err != nil {
			return err
		}
		uid, promotion, found := findPromotion(organization, identifier)
		if !found {
			return ErrNotFound
		}
		if promotion.Status != "pending" {
			return fmt.Errorf("%w: promotion %s is already %s", ErrConflict, promotion.ID, promotion.Status)
		}
		status := strings.ToLower(strings.TrimSpace(request.Status))
		if status != "approved" && status != "rejected" {
			return fmt.Errorf("promotion review status must be approved or rejected")
		}
		now := time.Now().UTC()
		promotion.Status = status
		promotion.ReviewNote = strings.TrimSpace(request.ReviewNote)
		promotion.ReviewedAt = now
		promotion.UpdatedAt = now
		if status == "approved" {
			decision, exists := organization.RepositoryDecisions[promotion.Repository][promotion.DecisionUID]
			if !exists || decision.ContentHash != promotion.DecisionHash {
				return fmt.Errorf("%w: decision %s changed after promotion was submitted", ErrConflict, promotion.DecisionID)
			}
			title := firstNonBlank(request.Title, decision.Title)
			statement := firstNonBlank(request.Statement, decision.Statement)
			scope := decision.Scope
			if len(request.Scope) > 0 {
				scope = model.NormalizeScope(request.Scope)
			}
			if title == "" || statement == "" || len(scope) == 0 {
				return fmt.Errorf("approved rule requires title, statement, and scope")
			}
			ruleUID, err := model.NewUID()
			if err != nil {
				return err
			}
			rule := model.Policy{
				SchemaVersion: model.CurrentSchemaVersion,
				ID:            nextRuleID(organization),
				UID:           ruleUID,
				Revision:      1,
				Title:         title,
				Statement:     statement,
				Scope:         scope,
				Status:        "active",
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := rule.RefreshContentHash(); err != nil {
				return err
			}
			organization.Rules[rule.UID] = rule
			promotion.RuleID = rule.ID
		}
		organization.Promotions[uid] = promotion
		organization.UpdatedAt = now
		result = promotion
		return nil
	})
	return result, err
}

func (s *FileStore) BuildContext(slug, repository, role, agent string) (model.ContextDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	organization, err := findOrganization(s.data, slug)
	if err != nil {
		return model.ContextDocument{}, err
	}
	repository = strings.TrimSpace(repository)
	decisions := mapValues(organization.RepositoryDecisions[repository])
	events := mapEventValues(organization.RepositoryEvents[repository])
	rules := mapPolicyValues(organization.Rules)
	return agentcontext.BuildDocument(organization.Name, repository, decisions, rules, events, role, agent)
}

func (s *FileStore) Snapshot(slug, repository string) (api.SnapshotResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	organization, err := findOrganization(s.data, slug)
	if err != nil {
		return api.SnapshotResponse{}, err
	}
	repository = strings.TrimSpace(repository)
	response := api.SnapshotResponse{
		Organization:       summaryFromOrganization(organization),
		Repository:         repository,
		Decisions:          mapValues(organization.RepositoryDecisions[repository]),
		Rules:              mapPolicyValues(organization.Rules),
		Events:             mapEventValues(organization.RepositoryEvents[repository]),
		ResolvedProposals:  make([]model.Proposal, 0),
		ResolvedPromotions: make([]model.Promotion, 0),
		GeneratedAt:        time.Now().UTC(),
	}
	for _, proposal := range organization.Proposals {
		if proposal.Repository == repository && (proposal.Status == "approved" || proposal.Status == "rejected") {
			response.ResolvedProposals = append(response.ResolvedProposals, proposal)
		}
	}
	for _, promotion := range organization.Promotions {
		if promotion.Repository == repository && (promotion.Status == "approved" || promotion.Status == "rejected") {
			response.ResolvedPromotions = append(response.ResolvedPromotions, promotion)
		}
	}
	sort.Slice(response.Decisions, func(i, j int) bool { return decisionBefore(response.Decisions[i].ID, response.Decisions[j].ID) })
	sort.Slice(response.Rules, func(i, j int) bool { return ruleBefore(response.Rules[i].ID, response.Rules[j].ID) })
	sort.Slice(response.Events, func(i, j int) bool { return response.Events[i].ID < response.Events[j].ID })
	sort.Slice(response.ResolvedProposals, func(i, j int) bool { return response.ResolvedProposals[i].ID < response.ResolvedProposals[j].ID })
	sort.Slice(response.ResolvedPromotions, func(i, j int) bool { return response.ResolvedPromotions[i].ID < response.ResolvedPromotions[j].ID })
	return response, nil
}

func (s *FileStore) RecordContext(slug string, input api.ContextRecordInput) (api.ContextRecord, error) {
	var record api.ContextRecord
	err := s.mutate(func(data *persistedState) error {
		organization, err := findOrganization(*data, slug)
		if err != nil {
			return err
		}
		if strings.TrimSpace(input.Agent) == "" || strings.TrimSpace(input.ContextHash) == "" {
			return fmt.Errorf("agent and context_hash are required")
		}
		uid, err := model.NewUID()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		recordedAt := input.RecordedAt.UTC()
		if recordedAt.IsZero() {
			recordedAt = now
		}
		record = api.ContextRecord{
			ID:          nextContextRecordID(organization),
			UID:         uid,
			Repository:  strings.TrimSpace(input.Repository),
			Agent:       strings.TrimSpace(input.Agent),
			Role:        strings.TrimSpace(input.Role),
			ContextHash: strings.TrimSpace(input.ContextHash),
			DecisionIDs: uniqueStrings(input.DecisionIDs),
			RuleIDs:     uniqueStrings(input.RuleIDs),
			EventIDs:    uniqueStrings(input.EventIDs),
			SessionID:   strings.TrimSpace(input.SessionID),
			Summary:     strings.TrimSpace(input.Summary),
			Source:      strings.TrimSpace(input.Source),
			RecordedAt:  recordedAt,
			ReceivedAt:  now,
		}
		organization.ContextRecords = append(organization.ContextRecords, record)
		organization.UpdatedAt = now
		return nil
	})
	return record, err
}

func (s *FileStore) mutate(change func(*persistedState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := clonePersistedState(s.data)
	if err != nil {
		return err
	}
	if err := change(&next); err != nil {
		return err
	}
	if err := writeJSONAtomic(s.path, next); err != nil {
		return fmt.Errorf("persist server data: %w", err)
	}
	s.data = next
	return nil
}

func newPersistedState() persistedState {
	return persistedState{SchemaVersion: dataSchemaVersion, Organizations: make(map[string]*organizationState)}
}

func normalizePersistedState(data *persistedState) {
	if data.Organizations == nil {
		data.Organizations = make(map[string]*organizationState)
	}
	for slug, organization := range data.Organizations {
		if organization == nil {
			delete(data.Organizations, slug)
			continue
		}
		if organization.Slug == "" {
			organization.Slug = slug
		}
		if organization.Name == "" {
			organization.Name = organization.Slug
		}
		if organization.Repositories == nil {
			organization.Repositories = make(map[string]api.RepositorySummary)
		}
		if organization.RepositoryDecisions == nil {
			organization.RepositoryDecisions = make(map[string]map[string]model.Decision)
		}
		if organization.RepositoryEvents == nil {
			organization.RepositoryEvents = make(map[string]map[string]model.Event)
		}
		if organization.Rules == nil {
			organization.Rules = make(map[string]model.Policy)
		}
		if organization.Proposals == nil {
			organization.Proposals = make(map[string]model.Proposal)
		}
		if organization.Promotions == nil {
			organization.Promotions = make(map[string]model.Promotion)
		}
		if organization.ContextRecords == nil {
			organization.ContextRecords = make([]api.ContextRecord, 0)
		}
		for uid, rule := range organization.LegacyPolicies {
			if _, exists := organization.Rules[uid]; !exists {
				organization.Rules[uid] = rule
			}
		}
		for uid, decision := range organization.LegacyDecisions {
			repository := firstNonBlank(organization.LegacyDecisionSource[uid], "legacy")
			if organization.RepositoryDecisions[repository] == nil {
				organization.RepositoryDecisions[repository] = make(map[string]model.Decision)
			}
			if _, exists := organization.RepositoryDecisions[repository][uid]; !exists {
				organization.RepositoryDecisions[repository][uid] = decision
			}
		}
		organization.LegacyDecisions = nil
		organization.LegacyDecisionSource = nil
		organization.LegacyPolicies = nil
		for repository := range organization.RepositoryDecisions {
			updateRepositorySummary(organization, repository, "", "", organization.UpdatedAt)
		}
		for repository := range organization.RepositoryEvents {
			updateRepositorySummary(organization, repository, "", "", organization.UpdatedAt)
		}
	}
}

func newOrganization(slug, name string, now time.Time) *organizationState {
	if strings.TrimSpace(name) == "" {
		name = slug
	}
	return &organizationState{
		Slug: slug, Name: name, CreatedAt: now, UpdatedAt: now,
		Repositories:        make(map[string]api.RepositorySummary),
		RepositoryDecisions: make(map[string]map[string]model.Decision),
		RepositoryEvents:    make(map[string]map[string]model.Event),
		Rules:               make(map[string]model.Policy), Proposals: make(map[string]model.Proposal), Promotions: make(map[string]model.Promotion),
		ContextRecords: make([]api.ContextRecord, 0),
	}
}

func findOrganization(data persistedState, slug string) (*organizationState, error) {
	normalized, err := organizationSlug(slug)
	if err != nil {
		return nil, err
	}
	organization := data.Organizations[normalized]
	if organization == nil {
		return nil, ErrNotFound
	}
	return organization, nil
}

func updateRepositorySummary(organization *organizationState, repository, source, repositoryURL string, now time.Time) {
	previous := organization.Repositories[repository]
	organization.Repositories[repository] = api.RepositorySummary{
		Name: repository, Source: firstNonBlank(source, previous.Source),
		RepositoryURL: firstNonBlank(repositoryURL, previous.RepositoryURL),
		DecisionCount: len(organization.RepositoryDecisions[repository]),
		EventCount:    len(organization.RepositoryEvents[repository]), LastSyncedAt: now,
	}
}

func summaryFromOrganization(organization *organizationState) api.OrganizationSummary {
	decisions, active, events := 0, 0, 0
	for _, items := range organization.RepositoryDecisions {
		decisions += len(items)
		for _, decision := range items {
			if strings.EqualFold(decision.Status, "active") {
				active++
			}
		}
	}
	for _, items := range organization.RepositoryEvents {
		events += len(items)
	}
	pendingProposals, pendingPromotions := 0, 0
	for _, proposal := range organization.Proposals {
		if proposal.Status == "pending" {
			pendingProposals++
		}
	}
	for _, promotion := range organization.Promotions {
		if promotion.Status == "pending" {
			pendingPromotions++
		}
	}
	return api.OrganizationSummary{
		Slug: organization.Slug, Name: organization.Name, CreatedAt: organization.CreatedAt, UpdatedAt: organization.UpdatedAt,
		RepositoryCount: len(organization.Repositories), DecisionCount: decisions, ActiveDecisionCount: active,
		RuleCount: len(organization.Rules), EventCount: events, ContextRecordCount: len(organization.ContextRecords), PendingProposalCount: pendingProposals, PendingPromotionCount: pendingPromotions,
	}
}

func detailFromOrganization(organization *organizationState) api.OrganizationDetail {
	decisionUsage := make(map[string]int)
	for _, record := range organization.ContextRecords {
		for _, id := range record.DecisionIDs {
			decisionUsage[id]++
		}
	}
	detail := api.OrganizationDetail{
		Organization: summaryFromOrganization(organization), Repositories: make([]api.RepositorySummary, 0, len(organization.Repositories)),
		Decisions: make([]api.DecisionView, 0), Rules: mapPolicyValues(organization.Rules), Events: make([]api.EventView, 0),
		Proposals:      make([]model.Proposal, 0, len(organization.Proposals)),
		Promotions:     make([]api.PromotionView, 0, len(organization.Promotions)),
		ContextRecords: append(make([]api.ContextRecord, 0, len(organization.ContextRecords)), organization.ContextRecords...),
	}
	for _, repository := range organization.Repositories {
		detail.Repositories = append(detail.Repositories, repository)
	}
	for repository, items := range organization.RepositoryDecisions {
		for _, decision := range items {
			detail.Decisions = append(detail.Decisions, api.DecisionView{Decision: decision, Repository: repository, ContextRecordCount: decisionUsage[decision.ID]})
		}
	}
	for repository, items := range organization.RepositoryEvents {
		for _, event := range items {
			detail.Events = append(detail.Events, api.EventView{Event: event, Repository: repository})
		}
	}
	for _, proposal := range organization.Proposals {
		detail.Proposals = append(detail.Proposals, proposal)
	}
	for _, promotion := range organization.Promotions {
		decision := organization.RepositoryDecisions[promotion.Repository][promotion.DecisionUID]
		detail.Promotions = append(detail.Promotions, api.PromotionView{Promotion: promotion, Decision: decision})
	}
	sort.Slice(detail.Repositories, func(i, j int) bool { return detail.Repositories[i].Name < detail.Repositories[j].Name })
	sort.Slice(detail.Decisions, func(i, j int) bool {
		if detail.Decisions[i].Repository == detail.Decisions[j].Repository {
			return decisionBefore(detail.Decisions[i].Decision.ID, detail.Decisions[j].Decision.ID)
		}
		return detail.Decisions[i].Repository < detail.Decisions[j].Repository
	})
	sort.Slice(detail.Rules, func(i, j int) bool { return ruleBefore(detail.Rules[i].ID, detail.Rules[j].ID) })
	sort.Slice(detail.Events, func(i, j int) bool {
		if detail.Events[i].Repository == detail.Events[j].Repository {
			return detail.Events[i].Event.ID < detail.Events[j].Event.ID
		}
		return detail.Events[i].Repository < detail.Events[j].Repository
	})
	sort.Slice(detail.Proposals, func(i, j int) bool {
		return detail.Proposals[i].UpdatedAt.After(detail.Proposals[j].UpdatedAt)
	})
	sort.Slice(detail.Promotions, func(i, j int) bool {
		return detail.Promotions[i].Promotion.UpdatedAt.After(detail.Promotions[j].Promotion.UpdatedAt)
	})
	sort.Slice(detail.ContextRecords, func(i, j int) bool {
		return detail.ContextRecords[i].ReceivedAt.After(detail.ContextRecords[j].ReceivedAt)
	})
	if len(detail.ContextRecords) > 100 {
		detail.ContextRecords = detail.ContextRecords[:100]
	}
	return detail
}

func findPromotion(organization *organizationState, identifier string) (string, model.Promotion, bool) {
	if promotion, exists := organization.Promotions[identifier]; exists {
		return identifier, promotion, true
	}
	for uid, promotion := range organization.Promotions {
		if promotion.ID == identifier || promotion.UID == identifier {
			return uid, promotion, true
		}
	}
	return "", model.Promotion{}, false
}

func findProposal(organization *organizationState, identifier string) (string, model.Proposal, bool) {
	if proposal, exists := organization.Proposals[identifier]; exists {
		return identifier, proposal, true
	}
	for uid, proposal := range organization.Proposals {
		if proposal.ID == identifier || proposal.UID == identifier {
			return uid, proposal, true
		}
	}
	return "", model.Proposal{}, false
}

func nextProposalID(organization *organizationState) string {
	maximum := 0
	for _, proposal := range organization.Proposals {
		if match := serverProposalIDPattern.FindStringSubmatch(proposal.ID); len(match) == 2 {
			if value, err := strconv.Atoi(match[1]); err == nil && value > maximum {
				maximum = value
			}
		}
	}
	return fmt.Sprintf("P-%03d", maximum+1)
}

func nextRepositoryDecisionID(decisions map[string]model.Decision) string {
	maximum := 0
	for _, decision := range decisions {
		if match := serverDecisionIDPattern.FindStringSubmatch(decision.ID); len(match) == 2 {
			if value, err := strconv.Atoi(match[1]); err == nil && value > maximum {
				maximum = value
			}
		}
	}
	return fmt.Sprintf("D-%03d", maximum+1)
}

func nextRuleID(organization *organizationState) string {
	maximum := 0
	for _, rule := range organization.Rules {
		if match := serverRuleIDPattern.FindStringSubmatch(rule.ID); len(match) == 2 {
			if value, err := strconv.Atoi(match[1]); err == nil && value > maximum {
				maximum = value
			}
		}
	}
	return fmt.Sprintf("R-%03d", maximum+1)
}
func nextPromotionID(organization *organizationState) string {
	maximum := 0
	for _, promotion := range organization.Promotions {
		if match := serverPromotionIDPattern.FindStringSubmatch(promotion.ID); len(match) == 2 {
			if value, err := strconv.Atoi(match[1]); err == nil && value > maximum {
				maximum = value
			}
		}
	}
	return fmt.Sprintf("M-%03d", maximum+1)
}
func nextContextRecordID(organization *organizationState) string {
	maximum := 0
	for _, record := range organization.ContextRecords {
		if match := serverContextRecordIDPattern.FindStringSubmatch(record.ID); len(match) == 2 {
			if value, err := strconv.Atoi(match[1]); err == nil && value > maximum {
				maximum = value
			}
		}
	}
	return fmt.Sprintf("CR-%06d", maximum+1)
}
func decisionBefore(left, right string) bool {
	return numberedBefore(left, right, regexp.MustCompile(`^D-(\d+)$`))
}
func ruleBefore(left, right string) bool { return numberedBefore(left, right, serverRuleIDPattern) }
func numberedBefore(left, right string, pattern *regexp.Regexp) bool {
	leftMatch, rightMatch := pattern.FindStringSubmatch(left), pattern.FindStringSubmatch(right)
	if len(leftMatch) == 2 && len(rightMatch) == 2 {
		leftNumber, _ := strconv.Atoi(leftMatch[1])
		rightNumber, _ := strconv.Atoi(rightMatch[1])
		return leftNumber < rightNumber
	}
	return left < right
}

func verifyDecisionHash(decision model.Decision) error {
	expected := decision
	if err := expected.RefreshContentHash(); err != nil {
		return err
	}
	if expected.ContentHash != decision.ContentHash {
		return fmt.Errorf("invalid decision %s: content_hash does not match content", decision.ID)
	}
	return nil
}
func verifyProposalHash(proposal model.Proposal) error {
	expected := proposal
	if err := expected.RefreshContentHash(); err != nil {
		return err
	}
	if expected.ContentHash != proposal.ContentHash {
		return fmt.Errorf("invalid proposal %s: content_hash does not match content", proposal.ID)
	}
	return nil
}
func verifyEventHash(event model.Event) error {
	expected := event
	if err := expected.RefreshContentHash(); err != nil {
		return err
	}
	if expected.ContentHash != event.ContentHash {
		return fmt.Errorf("invalid event %s: content_hash does not match content", event.ID)
	}
	return nil
}

func organizationSlug(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastSeparator := true
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			builder.WriteRune(character)
			lastSeparator = false
		} else if !lastSeparator {
			builder.WriteByte('-')
			lastSeparator = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "", fmt.Errorf("organization name must contain letters or numbers")
	}
	if len(slug) > 80 {
		return "", fmt.Errorf("organization slug must be at most 80 characters")
	}
	return slug, nil
}
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
func mapValues(items map[string]model.Decision) []model.Decision {
	result := make([]model.Decision, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result
}
func mapEventValues(items map[string]model.Event) []model.Event {
	result := make([]model.Event, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result
}
func mapPolicyValues(items map[string]model.Policy) []model.Policy {
	result := make([]model.Policy, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result
}
func clonePersistedState(data persistedState) (persistedState, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return persistedState{}, fmt.Errorf("clone server state: %w", err)
	}
	var clone persistedState
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return persistedState{}, fmt.Errorf("clone server state: %w", err)
	}
	normalizePersistedState(&clone)
	return clone, nil
}
func writeJSONAtomic(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".agc-server-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
