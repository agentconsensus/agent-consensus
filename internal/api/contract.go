// Package api contains the stable HTTP contract shared by agc clients, the
// server, and the embedded management dashboard.
package api

import (
	"time"

	"github.com/agent-consensus/ac/internal/model"
)

const Version = "v1"

// HealthResponse describes server availability and whether its API requires a
// bearer token. The endpoint itself stays public so the dashboard can present
// the appropriate connection UI before making an authenticated API request.
type HealthResponse struct {
	Status       string `json:"status"`
	APIVersion   string `json:"api_version"`
	AuthRequired bool   `json:"auth_required"`
}

type OrganizationSummary struct {
	Slug                  string    `json:"slug"`
	Name                  string    `json:"name"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
	RepositoryCount       int       `json:"repository_count"`
	DecisionCount         int       `json:"decision_count"`
	ActiveDecisionCount   int       `json:"active_decision_count"`
	RuleCount             int       `json:"rule_count"`
	EventCount            int       `json:"event_count"`
	ContextRecordCount    int       `json:"context_record_count"`
	PendingProposalCount  int       `json:"pending_proposal_count"`
	PendingPromotionCount int       `json:"pending_promotion_count"`
}

type RepositorySummary struct {
	Name          string    `json:"name"`
	Source        string    `json:"source,omitempty"`
	RepositoryURL string    `json:"repository_url,omitempty"`
	DecisionCount int       `json:"decision_count"`
	EventCount    int       `json:"event_count"`
	LastSyncedAt  time.Time `json:"last_synced_at"`
}

type DecisionView struct {
	Decision           model.Decision `json:"decision"`
	ContextRecordCount int            `json:"context_record_count"`
	// Repository owns the repository-scoped decision.
	Repository string `json:"repository,omitempty"`
}

type OrganizationDetail struct {
	Organization   OrganizationSummary `json:"organization"`
	Repositories   []RepositorySummary `json:"repositories"`
	Decisions      []DecisionView      `json:"decisions"`
	Rules          []model.Policy      `json:"rules"`
	Events         []EventView         `json:"events"`
	Proposals      []model.Proposal    `json:"proposals"`
	ContextRecords []ContextRecord     `json:"context_records"`
	Promotions     []PromotionView     `json:"promotions"`
}

// EventView pairs a repository case record with its source repository.
type EventView struct {
	Event      model.Event `json:"event"`
	Repository string      `json:"repository,omitempty"`
}

// PromotionView keeps the review queue self-contained: reviewers see the
// exact repository decision that is being considered for elevation.
type PromotionView struct {
	Promotion model.Promotion `json:"promotion"`
	Decision  model.Decision  `json:"decision"`
}

type CreateOrganizationRequest struct {
	Name string `json:"name"`
}

// SyncRequest is an immutable snapshot of one repository's local state. Rules
// are organization-only and cannot be submitted through this endpoint.
type SyncRequest struct {
	Organization string           `json:"organization,omitempty"`
	Repository   string           `json:"repository"`
	Source       string           `json:"source,omitempty"`
	// RepositoryURL optionally carries the repository's git remote so the
	// dashboard can link out to the hosting service.
	RepositoryURL string           `json:"repository_url,omitempty"`
	SentAt        time.Time        `json:"sent_at"`
	Decisions     []model.Decision `json:"decisions"`
	Events        []model.Event    `json:"events"`
}

type SyncConflict struct {
	UID    string `json:"uid"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type SyncResponse struct {
	Organization OrganizationSummary `json:"organization"`
	Applied      int                 `json:"applied"`
	Unchanged    int                 `json:"unchanged"`
	Conflicts    []SyncConflict      `json:"conflicts"`
	SyncedAt     time.Time           `json:"synced_at"`
}

// SnapshotResponse contains organization Rules plus the requesting
// repository's Decisions, Events and resolved promotion records.
type SnapshotResponse struct {
	Organization       OrganizationSummary `json:"organization"`
	Repository         string              `json:"repository,omitempty"`
	Decisions          []model.Decision    `json:"decisions"`
	Rules              []model.Policy      `json:"rules"`
	Events             []model.Event       `json:"events"`
	ResolvedProposals  []model.Proposal    `json:"resolved_proposals"`
	ResolvedPromotions []model.Promotion   `json:"resolved_promotions"`
	GeneratedAt        time.Time           `json:"generated_at"`
}

// SubmitPromotionRequest explicitly asks to elevate a previously synced
// repository decision into organization consensus. It never moves or removes
// the original repository decision.
type SubmitPromotionRequest struct {
	Repository string          `json:"repository"`
	Source     string          `json:"source,omitempty"`
	Promotion  model.Promotion `json:"promotion"`
}

// SubmitProposal asks for repository-level review of a temporary local
// decision. Approval creates a Decision only in the originating repository.
type SubmitProposalRequest struct {
	Repository string         `json:"repository"`
	Source     string         `json:"source,omitempty"`
	Proposal   model.Proposal `json:"proposal"`
}

type ReviewProposalRequest struct {
	Status     string   `json:"status"`
	ReviewNote string   `json:"review_note,omitempty"`
	Title      string   `json:"title,omitempty"`
	Statement  string   `json:"statement,omitempty"`
	Scope      []string `json:"scope,omitempty"`
	Owner      string   `json:"owner,omitempty"`
}

type ReviewPromotionRequest struct {
	Status     string   `json:"status"`
	ReviewNote string   `json:"review_note,omitempty"`
	Title      string   `json:"title,omitempty"`
	Statement  string   `json:"statement,omitempty"`
	Scope      []string `json:"scope,omitempty"`
}

// SubmitEventRequest shares a repository case record with the organization.
// Events are factual records: they are stored verbatim, scoped to their
// repository, and never go through promotion review.
type SubmitEventRequest struct {
	Repository string      `json:"repository"`
	Source     string      `json:"source,omitempty"`
	Event      model.Event `json:"event"`
}

// ContextRecordInput captures the exact compiled context an agent received.
// It is intentionally a record rather than agent memory: it provides audit
// evidence for which decisions influenced a later change or session.
type ContextRecordInput struct {
	Repository  string    `json:"repository,omitempty"`
	Agent       string    `json:"agent"`
	Role        string    `json:"role,omitempty"`
	ContextHash string    `json:"context_hash"`
	DecisionIDs []string  `json:"decision_ids"`
	RuleIDs     []string  `json:"rule_ids"`
	EventIDs    []string  `json:"event_ids"`
	SessionID   string    `json:"session_id,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Source      string    `json:"source,omitempty"`
	RecordedAt  time.Time `json:"recorded_at,omitempty"`
}

type ContextRecord struct {
	ID          string    `json:"id"`
	UID         string    `json:"uid"`
	Repository  string    `json:"repository,omitempty"`
	Agent       string    `json:"agent"`
	Role        string    `json:"role,omitempty"`
	ContextHash string    `json:"context_hash"`
	DecisionIDs []string  `json:"decision_ids"`
	RuleIDs     []string  `json:"rule_ids"`
	EventIDs    []string  `json:"event_ids"`
	SessionID   string    `json:"session_id,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Source      string    `json:"source,omitempty"`
	RecordedAt  time.Time `json:"recorded_at"`
	ReceivedAt  time.Time `json:"received_at"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
