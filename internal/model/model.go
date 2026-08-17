// Package model defines the versioned, portable files that make up an
// agent-consensus organization state.
package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const CurrentSchemaVersion = 1

// Config is stored in .agc/config.yaml.
type Config struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	Organization  string    `yaml:"organization" json:"organization"`
	Repository    string    `yaml:"repository" json:"repository"`
	// RepositoryURL is the git remote detected at init time; it lets the
	// dashboard link a repository back to its hosting service.
	RepositoryURL string    `yaml:"repository_url,omitempty" json:"repository_url,omitempty"`
	ServerURL     string    `yaml:"server_url,omitempty" json:"server_url,omitempty"`
	CreatedAt     time.Time `yaml:"created_at" json:"created_at"`
}

// Credentials stay in .agc/credentials.yaml, which the generated nested
// .gitignore excludes from source control. The server URL itself is safe to
// retain in Config so every developer can discover the shared organization.
type Credentials struct {
	AccessToken string `yaml:"access_token,omitempty" json:"access_token,omitempty"`
}

// Proposal is a temporary, repository-local decision candidate. It lives in
// .agc/local until a reviewer accepts it as a repository Decision. A pending
// remote proposal is never included in another repository's snapshot.
type Proposal struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	ID            string    `yaml:"id" json:"id"`
	UID           string    `yaml:"uid" json:"uid"`
	Kind          string    `yaml:"kind" json:"kind"`
	Title         string    `yaml:"title" json:"title"`
	Statement     string    `yaml:"statement" json:"statement"`
	Scope         []string  `yaml:"scope" json:"scope"`
	Owner         string    `yaml:"owner,omitempty" json:"owner,omitempty"`
	Repository    string    `yaml:"repository,omitempty" json:"repository,omitempty"`
	Status        string    `yaml:"status" json:"status"`
	RemoteID      string    `yaml:"remote_id,omitempty" json:"remote_id,omitempty"`
	DecisionID    string    `yaml:"decision_id,omitempty" json:"decision_id,omitempty"`
	ReviewNote    string    `yaml:"review_note,omitempty" json:"review_note,omitempty"`
	CreatedAt     time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time `yaml:"updated_at" json:"updated_at"`
	SubmittedAt   time.Time `yaml:"submitted_at,omitempty" json:"submitted_at,omitempty"`
	ReviewedAt    time.Time `yaml:"reviewed_at,omitempty" json:"reviewed_at,omitempty"`
	ContentHash   string    `yaml:"content_hash" json:"content_hash"`
}

// Decision is an active or historical engineering choice owned by one
// repository. Decisions never become organization state implicitly; a human
// must explicitly promote one to a Rule.
type Decision struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	ID            string    `yaml:"id" json:"id"`
	UID           string    `yaml:"uid" json:"uid"`
	Revision      int       `yaml:"revision" json:"revision"`
	Title         string    `yaml:"title" json:"title"`
	Statement     string    `yaml:"statement" json:"statement"`
	Scope         []string  `yaml:"scope" json:"scope"`
	Owner         string    `yaml:"owner" json:"owner"`
	Status        string    `yaml:"status" json:"status"`
	CreatedAt     time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time `yaml:"updated_at" json:"updated_at"`
	ContentHash   string    `yaml:"content_hash" json:"content_hash"`
}

// Policy is the persisted representation of an organization Rule. "Rule" is
// the product term; Policy remains the Go type to make its enforcement intent
// clear and to preserve the on-disk compatibility of early AGC state.
type Policy struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	ID            string    `yaml:"id" json:"id"`
	UID           string    `yaml:"uid" json:"uid"`
	Revision      int       `yaml:"revision" json:"revision"`
	Title         string    `yaml:"title" json:"title"`
	Statement     string    `yaml:"statement" json:"statement"`
	Scope         []string  `yaml:"scope" json:"scope"`
	Status        string    `yaml:"status" json:"status"`
	CreatedAt     time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time `yaml:"updated_at" json:"updated_at"`
	ContentHash   string    `yaml:"content_hash" json:"content_hash"`
}

// Promotion records an explicit request to elevate a repository decision into
// an organization Rule. The referenced decision stays in its repository after
// approval; RuleID is the resulting organization-level rule, if any.
type Promotion struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	ID            string    `yaml:"id" json:"id"`
	UID           string    `yaml:"uid" json:"uid"`
	DecisionID    string    `yaml:"decision_id" json:"decision_id"`
	DecisionUID   string    `yaml:"decision_uid" json:"decision_uid"`
	DecisionHash  string    `yaml:"decision_hash" json:"decision_hash"`
	Repository    string    `yaml:"repository,omitempty" json:"repository,omitempty"`
	Status        string    `yaml:"status" json:"status"`
	RemoteID      string    `yaml:"remote_id,omitempty" json:"remote_id,omitempty"`
	RuleID        string    `yaml:"rule_id,omitempty" json:"rule_id,omitempty"`
	ReviewNote    string    `yaml:"review_note,omitempty" json:"review_note,omitempty"`
	CreatedAt     time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time `yaml:"updated_at" json:"updated_at"`
	SubmittedAt   time.Time `yaml:"submitted_at,omitempty" json:"submitted_at,omitempty"`
	ReviewedAt    time.Time `yaml:"reviewed_at,omitempty" json:"reviewed_at,omitempty"`
}

// Event is a repository-level case record: something that actually happened
// and is kept as precedent knowledge for agents. Unlike decisions, events are
// factual records — they are shared verbatim, never reviewed, and never
// promoted to organization-level consensus.
type Event struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	ID            string    `yaml:"id" json:"id"`
	UID           string    `yaml:"uid" json:"uid"`
	Title         string    `yaml:"title" json:"title"`
	Statement     string    `yaml:"statement" json:"statement"`
	Scope         []string  `yaml:"scope" json:"scope"`
	CreatedAt     time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time `yaml:"updated_at" json:"updated_at"`
	ContentHash   string    `yaml:"content_hash" json:"content_hash"`
}

// ContextDocument is the compiled, role-specific state consumed by an agent.
type ContextDocument struct {
	Organization string        `json:"organization"`
	Repository   string        `json:"repository"`
	Role         string        `json:"role"`
	Agent        string        `json:"agent,omitempty"`
	Decisions    []ContextItem `json:"decisions"`
	Rules        []ContextItem `json:"rules"`
	Events       []ContextItem `json:"events"`
	ContextHash  string        `json:"context_hash"`
}

// ContextItem intentionally exposes only the fields an agent needs to follow
// a consensus item, rather than implementation metadata.
type ContextItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Statement string   `json:"statement"`
	Scope     []string `json:"scope"`
}

func (c Config) Validate() error {
	if c.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported config schema version %d", c.SchemaVersion)
	}
	if strings.TrimSpace(c.Organization) == "" {
		return fmt.Errorf("organization is required")
	}
	if strings.TrimSpace(c.Repository) == "" {
		return fmt.Errorf("repository is required")
	}
	return nil
}

func (d Decision) Validate() error {
	if d.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported decision schema version %d", d.SchemaVersion)
	}
	if strings.TrimSpace(d.ID) == "" {
		return fmt.Errorf("decision id is required")
	}
	if strings.TrimSpace(d.UID) == "" {
		return fmt.Errorf("decision uid is required")
	}
	if d.Revision < 1 {
		return fmt.Errorf("decision revision must be at least 1")
	}
	if strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("decision title is required")
	}
	if strings.TrimSpace(d.Statement) == "" {
		return fmt.Errorf("decision statement is required")
	}
	if len(d.Scope) == 0 {
		return fmt.Errorf("decision scope is required")
	}
	if strings.TrimSpace(d.Status) == "" {
		return fmt.Errorf("decision status is required")
	}
	return nil
}

func (p Policy) Validate() error {
	if p.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported policy schema version %d", p.SchemaVersion)
	}
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.UID) == "" || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Statement) == "" {
		return fmt.Errorf("policy id, uid, title, and statement are required")
	}
	if p.Revision < 1 || len(p.Scope) == 0 || strings.TrimSpace(p.Status) == "" {
		return fmt.Errorf("policy revision, scope, and status are required")
	}
	return nil
}

func (p Promotion) Validate() error {
	if p.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported promotion schema version %d", p.SchemaVersion)
	}
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.UID) == "" {
		return fmt.Errorf("promotion id and uid are required")
	}
	if strings.TrimSpace(p.DecisionID) == "" || strings.TrimSpace(p.DecisionUID) == "" || strings.TrimSpace(p.DecisionHash) == "" {
		return fmt.Errorf("promotion decision id, uid, and hash are required")
	}
	switch strings.ToLower(strings.TrimSpace(p.Status)) {
	case "local", "submitted", "pending", "approved", "rejected":
	default:
		return fmt.Errorf("unsupported promotion status %q", p.Status)
	}
	return nil
}

func (p Proposal) Validate() error {
	if p.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported proposal schema version %d", p.SchemaVersion)
	}
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.UID) == "" {
		return fmt.Errorf("proposal id and uid are required")
	}
	if strings.ToLower(strings.TrimSpace(p.Kind)) != "decision" {
		return fmt.Errorf("unsupported proposal kind %q", p.Kind)
	}
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Statement) == "" || len(p.Scope) == 0 {
		return fmt.Errorf("proposal title, statement, and scope are required")
	}
	switch strings.ToLower(strings.TrimSpace(p.Status)) {
	case "local", "submitted", "pending", "approved", "rejected":
	default:
		return fmt.Errorf("unsupported proposal status %q", p.Status)
	}
	return nil
}

func (e Event) Validate() error {
	if e.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported event schema version %d", e.SchemaVersion)
	}
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.UID) == "" {
		return fmt.Errorf("event id and uid are required")
	}
	if strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Statement) == "" {
		return fmt.Errorf("event title and statement are required")
	}
	if len(e.Scope) == 0 {
		return fmt.Errorf("event scope is required")
	}
	return nil
}

// RefreshContentHash hashes the stable case content. Events are immutable
// records, so the hash covers identity and content but not the hash itself.
func (e *Event) RefreshContentHash() error {
	payload := struct {
		ID        string    `json:"id"`
		UID       string    `json:"uid"`
		Title     string    `json:"title"`
		Statement string    `json:"statement"`
		Scope     []string  `json:"scope"`
		CreatedAt time.Time `json:"created_at"`
	}{e.ID, e.UID, e.Title, e.Statement, e.Scope, e.CreatedAt}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("serialize event for hashing: %w", err)
	}
	sum := sha256.Sum256(encoded)
	e.ContentHash = "sha256:" + hex.EncodeToString(sum[:])
	return nil
}

// RefreshContentHash computes the stable content hash for an organization
// rule. Rule identifiers and lifecycle fields are included so revisions remain
// conflict-safe when pulled into repositories.
func (p *Policy) RefreshContentHash() error {
	payload := struct {
		SchemaVersion int       `json:"schema_version"`
		ID            string    `json:"id"`
		UID           string    `json:"uid"`
		Revision      int       `json:"revision"`
		Title         string    `json:"title"`
		Statement     string    `json:"statement"`
		Scope         []string  `json:"scope"`
		Status        string    `json:"status"`
		CreatedAt     time.Time `json:"created_at"`
		UpdatedAt     time.Time `json:"updated_at"`
	}{p.SchemaVersion, p.ID, p.UID, p.Revision, p.Title, p.Statement, p.Scope, p.Status, p.CreatedAt, p.UpdatedAt}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("serialize policy for hashing: %w", err)
	}
	sum := sha256.Sum256(encoded)
	p.ContentHash = "sha256:" + hex.EncodeToString(sum[:])
	return nil
}

// RefreshContentHash intentionally excludes review lifecycle metadata, so a
// retry can be recognized as the same local candidate.
func (p *Proposal) RefreshContentHash() error {
	payload := struct {
		Kind      string   `json:"kind"`
		Title     string   `json:"title"`
		Statement string   `json:"statement"`
		Scope     []string `json:"scope"`
		Owner     string   `json:"owner"`
	}{p.Kind, p.Title, p.Statement, p.Scope, p.Owner}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("serialize proposal for hashing: %w", err)
	}
	sum := sha256.Sum256(encoded)
	p.ContentHash = "sha256:" + hex.EncodeToString(sum[:])
	return nil
}

// NormalizeScope makes matching deterministic across CLI input and file edits.
func NormalizeScope(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
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

// ScopeMatches returns true for global items and items explicitly relevant to a role.
func ScopeMatches(scope []string, role string) bool {
	if len(scope) == 0 {
		return true
	}
	role = strings.ToLower(strings.TrimSpace(role))
	// Without a role, context is intentionally organization-wide. This keeps
	// `agc context` useful for generic agents while `--role` narrows it.
	if role == "" {
		return true
	}
	for _, target := range scope {
		target = strings.ToLower(strings.TrimSpace(target))
		if target == "all" || target == "*" || target == role {
			return true
		}
	}
	return false
}

// NewUID returns a random RFC 4122 version 4 identifier suitable for syncing
// the item later even if its human-readable ID changes.
func NewUID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate uid: %w", err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

// RefreshContentHash computes a stable hash of all decision content except the
// hash itself. It is deliberately based on JSON rather than YAML formatting.
func (d *Decision) RefreshContentHash() error {
	payload := struct {
		SchemaVersion int       `json:"schema_version"`
		ID            string    `json:"id"`
		UID           string    `json:"uid"`
		Revision      int       `json:"revision"`
		Title         string    `json:"title"`
		Statement     string    `json:"statement"`
		Scope         []string  `json:"scope"`
		Owner         string    `json:"owner"`
		Status        string    `json:"status"`
		CreatedAt     time.Time `json:"created_at"`
		UpdatedAt     time.Time `json:"updated_at"`
	}{d.SchemaVersion, d.ID, d.UID, d.Revision, d.Title, d.Statement, d.Scope, d.Owner, d.Status, d.CreatedAt, d.UpdatedAt}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("serialize decision for hashing: %w", err)
	}
	sum := sha256.Sum256(encoded)
	d.ContentHash = "sha256:" + hex.EncodeToString(sum[:])
	return nil
}

// AsLocalDecision makes a temporary proposal available to the current
// repository's agent context while it awaits repository-level review.
func (p Proposal) AsLocalDecision() (Decision, error) {
	decision := Decision{SchemaVersion: CurrentSchemaVersion, ID: p.ID, UID: p.UID, Revision: 1, Title: p.Title, Statement: p.Statement, Scope: NormalizeScope(p.Scope), Owner: p.Owner, Status: "active", CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if err := decision.Validate(); err != nil {
		return Decision{}, err
	}
	if err := decision.RefreshContentHash(); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func ContextHash(document ContextDocument) (string, error) {
	document.ContextHash = ""
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("serialize context for hashing: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
