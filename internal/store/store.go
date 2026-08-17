// Package store manages the on-disk .agc organization state.
package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agent-consensus/ac/internal/model"
	"gopkg.in/yaml.v3"
)

const DirectoryName = ".agc"

var decisionIDPattern = regexp.MustCompile(`^D-(\d+)$`)
var proposalIDPattern = regexp.MustCompile(`^L-(\d+)$`)
var eventIDPattern = regexp.MustCompile(`^E-(\d+)$`)
var promotionIDPattern = regexp.MustCompile(`^M-(\d+)$`)

type State struct {
	Root string
}

func New(root string) State {
	return State{Root: root}
}

func (s State) ConfigPath() string   { return filepath.Join(s.Root, "config.yaml") }
func (s State) DecisionsDir() string { return filepath.Join(s.Root, "decisions") }
func (s State) RulesDir() string     { return filepath.Join(s.Root, "rules") }

// PoliciesDir is kept as an internal compatibility alias. Rules are the
// organization-level Policy entities; repositories should use RulesDir.
func (s State) PoliciesDir() string   { return s.RulesDir() }
func (s State) EventsDir() string     { return filepath.Join(s.Root, "events") }
func (s State) PromotionsDir() string { return filepath.Join(s.Root, "promotions") }
func (s State) KnowledgeDir() string  { return filepath.Join(s.Root, "knowledge") }
func (s State) SessionsDir() string   { return filepath.Join(s.Root, "sessions") }
func (s State) ContextDir() string    { return filepath.Join(s.Root, "context") }
func (s State) LocalDir() string      { return filepath.Join(s.Root, "local") }
func (s State) CredentialsPath() string {
	return filepath.Join(s.Root, "credentials.yaml")
}
func (s State) LocalGitIgnorePath() string { return filepath.Join(s.Root, ".gitignore") }

// EnsureLayout upgrades an existing .agc directory to include files added by
// newer local-first workflows. It never rewrites config.yaml or existing
// state files, so calling it from init/login is safe for an established
// repository.
func (s State) EnsureLayout() error {
	for _, directory := range []string{s.DecisionsDir(), s.RulesDir(), s.EventsDir(), s.PromotionsDir(), s.LocalDir()} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create local state directory: %w", err)
		}
	}
	if _, err := os.Stat(s.LocalGitIgnorePath()); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect local gitignore: %w", err)
	}
	if err := os.WriteFile(s.LocalGitIgnorePath(), []byte("credentials.yaml\n"), 0o644); err != nil {
		return fmt.Errorf("write local gitignore: %w", err)
	}
	return nil
}

// Initialize creates a complete local organization state. State files are
// intentionally ordinary files so they can be reviewed and synchronized by Git.
func Initialize(root string, config model.Config) (State, error) {
	state := New(root)
	if err := config.Validate(); err != nil {
		return State{}, err
	}
	if _, err := os.Stat(root); err == nil {
		return State{}, fmt.Errorf("%s already exists", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return State{}, fmt.Errorf("inspect state directory: %w", err)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return State{}, fmt.Errorf("create state directory: %w", err)
	}
	for _, directory := range []string{state.DecisionsDir(), state.RulesDir(), state.EventsDir(), state.PromotionsDir(), state.KnowledgeDir(), state.SessionsDir(), state.ContextDir(), state.LocalDir()} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			return State{}, fmt.Errorf("create %s: %w", directory, err)
		}
	}
	if err := os.WriteFile(state.LocalGitIgnorePath(), []byte("credentials.yaml\n"), 0o644); err != nil {
		return State{}, fmt.Errorf("write local gitignore: %w", err)
	}
	if err := writeYAML(state.ConfigPath(), config); err != nil {
		return State{}, fmt.Errorf("write config: %w", err)
	}
	return state, nil
}

// Find walks upward so an agent can invoke agc from a package subdirectory.
func Find(start string) (State, error) {
	directory, err := filepath.Abs(start)
	if err != nil {
		return State{}, fmt.Errorf("resolve working directory: %w", err)
	}
	for {
		candidate := filepath.Join(directory, DirectoryName)
		info, statErr := os.Stat(candidate)
		if statErr == nil && info.IsDir() {
			return New(candidate), nil
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return State{}, fmt.Errorf("inspect %s: %w", candidate, statErr)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return State{}, fmt.Errorf("no %s directory found; run agc init first", DirectoryName)
}

func (s State) LoadConfig() (model.Config, error) {
	var config model.Config
	if err := readYAML(s.ConfigPath(), &config); err != nil {
		return model.Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return model.Config{}, fmt.Errorf("validate config: %w", err)
	}
	return config, nil
}

// SaveRepositoryURL records the detected git remote in the reviewable config
// so it can be synced to the server and linked from the dashboard.
func (s State) SaveRepositoryURL(repositoryURL string) (model.Config, error) {
	if err := s.EnsureLayout(); err != nil {
		return model.Config{}, err
	}
	config, err := s.LoadConfig()
	if err != nil {
		return model.Config{}, err
	}
	config.RepositoryURL = strings.TrimSpace(repositoryURL)
	if err := writeYAML(s.ConfigPath(), config); err != nil {
		return model.Config{}, fmt.Errorf("write config: %w", err)
	}
	return config, nil
}

// SaveConnection records the shared server location in the reviewable config
// and keeps its access token in the nested Git-ignored credentials file.
func (s State) SaveConnection(serverURL, accessToken string) (model.Config, error) {
	if err := s.EnsureLayout(); err != nil {
		return model.Config{}, err
	}
	config, err := s.LoadConfig()
	if err != nil {
		return model.Config{}, err
	}
	config.ServerURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if err := writeYAML(s.ConfigPath(), config); err != nil {
		return model.Config{}, fmt.Errorf("write config: %w", err)
	}
	credentials := model.Credentials{AccessToken: strings.TrimSpace(accessToken)}
	if credentials.AccessToken == "" {
		if err := os.Remove(s.CredentialsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return model.Config{}, fmt.Errorf("remove previous credentials: %w", err)
		}
		return config, nil
	}
	if err := writeYAML(s.CredentialsPath(), credentials); err != nil {
		return model.Config{}, fmt.Errorf("write credentials: %w", err)
	}
	if err := os.Chmod(s.CredentialsPath(), 0o600); err != nil {
		return model.Config{}, fmt.Errorf("restrict credentials permissions: %w", err)
	}
	return config, nil
}

func (s State) LoadCredentials() (model.Credentials, error) {
	var credentials model.Credentials
	if err := readYAML(s.CredentialsPath(), &credentials); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return model.Credentials{}, nil
		}
		return model.Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	credentials.AccessToken = strings.TrimSpace(credentials.AccessToken)
	return credentials, nil
}

func (s State) ListDecisions() ([]model.Decision, error) {
	entries, err := os.ReadDir(s.DecisionsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []model.Decision{}, nil
		}
		return nil, fmt.Errorf("read decisions: %w", err)
	}
	decisions := make([]model.Decision, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(s.DecisionsDir(), entry.Name())
		var decision model.Decision
		if err := readYAML(path, &decision); err != nil {
			return nil, fmt.Errorf("read decision %s: %w", entry.Name(), err)
		}
		if err := decision.Validate(); err != nil {
			return nil, fmt.Errorf("validate decision %s: %w", entry.Name(), err)
		}
		decisions = append(decisions, decision)
	}
	sort.Slice(decisions, func(i, j int) bool { return decisionLess(decisions[i].ID, decisions[j].ID) })
	return decisions, nil
}

func (s State) ListRules() ([]model.Policy, error) {
	entries, err := os.ReadDir(s.RulesDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []model.Policy{}, nil
		}
		return nil, fmt.Errorf("read policies: %w", err)
	}
	policies := make([]model.Policy, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(s.RulesDir(), entry.Name())
		var policy model.Policy
		if err := readYAML(path, &policy); err != nil {
			return nil, fmt.Errorf("read policy %s: %w", entry.Name(), err)
		}
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("validate policy %s: %w", entry.Name(), err)
		}
		policies = append(policies, policy)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID < policies[j].ID })
	return policies, nil
}

// ListPolicies is a source-compatible alias for ListRules.
func (s State) ListPolicies() ([]model.Policy, error) { return s.ListRules() }

func (s State) ListEvents() ([]model.Event, error) {
	entries, err := os.ReadDir(s.EventsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []model.Event{}, nil
		}
		return nil, fmt.Errorf("read events: %w", err)
	}
	events := make([]model.Event, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		var event model.Event
		if err := readYAML(filepath.Join(s.EventsDir(), entry.Name()), &event); err != nil {
			return nil, fmt.Errorf("read event %s: %w", entry.Name(), err)
		}
		event.Scope = model.NormalizeScope(event.Scope)
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("validate event %s: %w", entry.Name(), err)
		}
		if err := validateEventHash(event); err != nil {
			return nil, fmt.Errorf("validate event %s: %w", entry.Name(), err)
		}
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool { return eventLess(events[i].ID, events[j].ID) })
	return events, nil
}

func (s State) ListProposals() ([]model.Proposal, error) {
	entries, err := os.ReadDir(s.LocalDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []model.Proposal{}, nil
		}
		return nil, fmt.Errorf("read local decisions: %w", err)
	}
	proposals := make([]model.Proposal, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		var proposal model.Proposal
		if err := readYAML(filepath.Join(s.LocalDir(), entry.Name()), &proposal); err != nil {
			return nil, fmt.Errorf("read local decision %s: %w", entry.Name(), err)
		}
		proposal.Scope = model.NormalizeScope(proposal.Scope)
		if err := proposal.Validate(); err != nil {
			return nil, fmt.Errorf("validate local decision %s: %w", entry.Name(), err)
		}
		if err := validateProposalHash(proposal); err != nil {
			return nil, fmt.Errorf("validate local decision %s: %w", entry.Name(), err)
		}
		proposals = append(proposals, proposal)
	}
	sort.Slice(proposals, func(i, j int) bool { return proposalLess(proposals[i].ID, proposals[j].ID) })
	return proposals, nil
}

// CreateProposal writes a temporary local decision. It becomes a repository
// Decision only after remote review succeeds.
func (s State) CreateProposal(title, statement string, scope []string, owner string) (model.Proposal, error) {
	if err := s.EnsureLayout(); err != nil {
		return model.Proposal{}, err
	}
	proposals, err := s.ListProposals()
	if err != nil {
		return model.Proposal{}, err
	}
	uid, err := model.NewUID()
	if err != nil {
		return model.Proposal{}, err
	}
	now := time.Now().UTC()
	proposal := model.Proposal{SchemaVersion: model.CurrentSchemaVersion, ID: nextProposalID(proposals), UID: uid, Kind: "decision", Title: strings.TrimSpace(title), Statement: strings.TrimSpace(statement), Scope: model.NormalizeScope(scope), Owner: strings.TrimSpace(owner), Status: "local", CreatedAt: now, UpdatedAt: now}
	if err := proposal.Validate(); err != nil {
		return model.Proposal{}, err
	}
	if err := proposal.RefreshContentHash(); err != nil {
		return model.Proposal{}, err
	}
	if err := writeYAML(filepath.Join(s.LocalDir(), proposal.ID+".yaml"), proposal); err != nil {
		return model.Proposal{}, fmt.Errorf("write local decision: %w", err)
	}
	return proposal, nil
}

func (s State) MarkProposalSubmitted(uid string, remote model.Proposal) (model.Proposal, error) {
	proposals, err := s.ListProposals()
	if err != nil {
		return model.Proposal{}, err
	}
	for _, proposal := range proposals {
		if proposal.UID != strings.TrimSpace(uid) {
			continue
		}
		proposal.Status = "submitted"
		proposal.RemoteID = remote.ID
		proposal.Repository = remote.Repository
		proposal.SubmittedAt = remote.SubmittedAt
		if proposal.SubmittedAt.IsZero() {
			proposal.SubmittedAt = time.Now().UTC()
		}
		proposal.UpdatedAt = time.Now().UTC()
		if err := writeYAML(filepath.Join(s.LocalDir(), proposal.ID+".yaml"), proposal); err != nil {
			return model.Proposal{}, fmt.Errorf("write submitted local decision: %w", err)
		}
		return proposal, nil
	}
	return model.Proposal{}, fmt.Errorf("local decision with uid %s was not found", uid)
}

func (s State) ListEffectiveLocalDecisions() ([]model.Decision, error) {
	proposals, err := s.ListProposals()
	if err != nil {
		return nil, err
	}
	decisions := make([]model.Decision, 0, len(proposals))
	for _, proposal := range proposals {
		if proposal.Status == "approved" {
			continue
		}
		decision, err := proposal.AsLocalDecision()
		if err != nil {
			return nil, fmt.Errorf("convert local decision %s: %w", proposal.ID, err)
		}
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

// ApplyResolvedProposals removes only approved local candidates. Rejected
// candidates deliberately remain in .agc/local as temporary local rules.
func (s State) ApplyResolvedProposals(remoteProposals []model.Proposal) (int, error) {
	proposals, err := s.ListProposals()
	if err != nil {
		return 0, err
	}
	byUID := make(map[string]model.Proposal, len(proposals))
	for _, proposal := range proposals {
		byUID[proposal.UID] = proposal
	}
	changed := 0
	for _, remote := range remoteProposals {
		local, exists := byUID[remote.UID]
		if !exists {
			continue
		}
		switch remote.Status {
		case "approved":
			if err := os.Remove(filepath.Join(s.LocalDir(), local.ID+".yaml")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return changed, fmt.Errorf("remove approved local decision %s: %w", local.ID, err)
			}
			changed++
		case "rejected":
			if local.Status == "rejected" && local.ReviewNote == remote.ReviewNote {
				continue
			}
			local.Status, local.ReviewNote, local.DecisionID = "rejected", remote.ReviewNote, remote.DecisionID
			local.ReviewedAt, local.UpdatedAt = remote.ReviewedAt, remote.UpdatedAt
			if err := writeYAML(filepath.Join(s.LocalDir(), local.ID+".yaml"), local); err != nil {
				return changed, fmt.Errorf("write rejected local decision: %w", err)
			}
			changed++
		}
	}
	return changed, nil
}

// CreateDecision assigns the next local D-### identifier and writes a complete
// versioned decision file.
func (s State) CreateDecision(title, statement string, scope []string, owner string) (model.Decision, error) {
	if err := s.EnsureLayout(); err != nil {
		return model.Decision{}, err
	}
	decisions, err := s.ListDecisions()
	if err != nil {
		return model.Decision{}, err
	}
	id := nextDecisionID(decisions)
	uid, err := model.NewUID()
	if err != nil {
		return model.Decision{}, err
	}
	now := time.Now().UTC()
	decision := model.Decision{
		SchemaVersion: model.CurrentSchemaVersion,
		ID:            id,
		UID:           uid,
		Revision:      1,
		Title:         strings.TrimSpace(title),
		Statement:     strings.TrimSpace(statement),
		Scope:         model.NormalizeScope(scope),
		Owner:         strings.TrimSpace(owner),
		Status:        "active",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := decision.Validate(); err != nil {
		return model.Decision{}, err
	}
	if err := decision.RefreshContentHash(); err != nil {
		return model.Decision{}, err
	}
	path := filepath.Join(s.DecisionsDir(), decision.ID+".yaml")
	if _, err := os.Stat(path); err == nil {
		return model.Decision{}, fmt.Errorf("decision file %s already exists", filepath.Base(path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return model.Decision{}, fmt.Errorf("inspect decision path: %w", err)
	}
	if err := writeYAML(path, decision); err != nil {
		return model.Decision{}, fmt.Errorf("write decision: %w", err)
	}
	return decision, nil
}

// CreateEvent writes a repository-local case record. Events are factual
// precedents, not candidates for organization consensus.
func (s State) CreateEvent(title, statement string, scope []string) (model.Event, error) {
	if err := s.EnsureLayout(); err != nil {
		return model.Event{}, err
	}
	events, err := s.ListEvents()
	if err != nil {
		return model.Event{}, err
	}
	uid, err := model.NewUID()
	if err != nil {
		return model.Event{}, err
	}
	now := time.Now().UTC()
	event := model.Event{
		SchemaVersion: model.CurrentSchemaVersion,
		ID:            nextEventID(events),
		UID:           uid,
		Title:         strings.TrimSpace(title),
		Statement:     strings.TrimSpace(statement),
		Scope:         model.NormalizeScope(scope),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := event.Validate(); err != nil {
		return model.Event{}, err
	}
	if err := event.RefreshContentHash(); err != nil {
		return model.Event{}, err
	}
	if err := writeYAML(filepath.Join(s.EventsDir(), event.ID+".yaml"), event); err != nil {
		return model.Event{}, fmt.Errorf("write event: %w", err)
	}
	return event, nil
}

func (s State) ListPromotions() ([]model.Promotion, error) {
	entries, err := os.ReadDir(s.PromotionsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []model.Promotion{}, nil
		}
		return nil, fmt.Errorf("read promotions: %w", err)
	}
	promotions := make([]model.Promotion, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		var promotion model.Promotion
		if err := readYAML(filepath.Join(s.PromotionsDir(), entry.Name()), &promotion); err != nil {
			return nil, fmt.Errorf("read promotion %s: %w", entry.Name(), err)
		}
		if err := promotion.Validate(); err != nil {
			return nil, fmt.Errorf("validate promotion %s: %w", entry.Name(), err)
		}
		promotions = append(promotions, promotion)
	}
	sort.Slice(promotions, func(i, j int) bool { return promotionLess(promotions[i].ID, promotions[j].ID) })
	return promotions, nil
}

// CreatePromotion marks a repository decision for intentional escalation. It
// does not send anything remotely; callers use SubmitPromotion explicitly.
func (s State) CreatePromotion(identifier string) (model.Promotion, error) {
	if err := s.EnsureLayout(); err != nil {
		return model.Promotion{}, err
	}
	decisions, err := s.ListDecisions()
	if err != nil {
		return model.Promotion{}, err
	}
	var decision model.Decision
	found := false
	for _, candidate := range decisions {
		if candidate.ID == strings.TrimSpace(identifier) || candidate.UID == strings.TrimSpace(identifier) {
			decision, found = candidate, true
			break
		}
	}
	if !found {
		return model.Promotion{}, fmt.Errorf("repository decision %s was not found", identifier)
	}
	promotions, err := s.ListPromotions()
	if err != nil {
		return model.Promotion{}, err
	}
	for _, existing := range promotions {
		if existing.DecisionUID == decision.UID && existing.Status == "local" {
			return existing, nil
		}
		if existing.DecisionUID == decision.UID && (existing.Status == "submitted" || existing.Status == "pending") {
			return model.Promotion{}, fmt.Errorf("decision %s already has promotion %s (%s)", decision.ID, existing.ID, existing.Status)
		}
	}
	uid, err := model.NewUID()
	if err != nil {
		return model.Promotion{}, err
	}
	now := time.Now().UTC()
	promotion := model.Promotion{
		SchemaVersion: model.CurrentSchemaVersion,
		ID:            nextPromotionID(promotions),
		UID:           uid,
		DecisionID:    decision.ID,
		DecisionUID:   decision.UID,
		DecisionHash:  decision.ContentHash,
		Status:        "local",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := promotion.Validate(); err != nil {
		return model.Promotion{}, err
	}
	if err := writeYAML(filepath.Join(s.PromotionsDir(), promotion.ID+".yaml"), promotion); err != nil {
		return model.Promotion{}, fmt.Errorf("write promotion: %w", err)
	}
	return promotion, nil
}

func (s State) MarkPromotionSubmitted(uid string, remote model.Promotion) (model.Promotion, error) {
	promotions, err := s.ListPromotions()
	if err != nil {
		return model.Promotion{}, err
	}
	for _, promotion := range promotions {
		if promotion.UID != strings.TrimSpace(uid) {
			continue
		}
		promotion.Status = remote.Status
		promotion.RemoteID = remote.ID
		promotion.Repository = remote.Repository
		promotion.SubmittedAt = remote.SubmittedAt
		promotion.UpdatedAt = remote.UpdatedAt
		if promotion.UpdatedAt.IsZero() {
			promotion.UpdatedAt = time.Now().UTC()
		}
		if err := writeYAML(filepath.Join(s.PromotionsDir(), promotion.ID+".yaml"), promotion); err != nil {
			return model.Promotion{}, fmt.Errorf("write submitted promotion: %w", err)
		}
		return promotion, nil
	}
	return model.Promotion{}, fmt.Errorf("local promotion with uid %s was not found", uid)
}

// ApplyResolvedPromotions updates the local audit trail while deliberately
// keeping the referenced repository decisions intact.
func (s State) ApplyResolvedPromotions(remotePromotions []model.Promotion) (int, error) {
	promotions, err := s.ListPromotions()
	if err != nil {
		return 0, err
	}
	byUID := make(map[string]model.Promotion, len(promotions))
	for _, promotion := range promotions {
		byUID[promotion.UID] = promotion
	}
	updated := 0
	for _, remote := range remotePromotions {
		local, exists := byUID[remote.UID]
		if !exists || (remote.Status != "approved" && remote.Status != "rejected") {
			continue
		}
		if local.Status == remote.Status && local.RuleID == remote.RuleID && local.ReviewNote == remote.ReviewNote {
			continue
		}
		local.Status = remote.Status
		local.RuleID = remote.RuleID
		local.ReviewNote = remote.ReviewNote
		local.Repository = remote.Repository
		local.ReviewedAt = remote.ReviewedAt
		local.UpdatedAt = remote.UpdatedAt
		if err := writeYAML(filepath.Join(s.PromotionsDir(), local.ID+".yaml"), local); err != nil {
			return updated, fmt.Errorf("write resolved promotion: %w", err)
		}
		updated++
	}
	return updated, nil
}

func nextDecisionID(decisions []model.Decision) string {
	maximum := 0
	for _, decision := range decisions {
		matches := decisionIDPattern.FindStringSubmatch(decision.ID)
		if len(matches) != 2 {
			continue
		}
		value, err := strconv.Atoi(matches[1])
		if err == nil && value > maximum {
			maximum = value
		}
	}
	return fmt.Sprintf("D-%03d", maximum+1)
}

func nextProposalID(proposals []model.Proposal) string {
	maximum := 0
	for _, proposal := range proposals {
		matches := proposalIDPattern.FindStringSubmatch(proposal.ID)
		if len(matches) != 2 {
			continue
		}
		value, err := strconv.Atoi(matches[1])
		if err == nil && value > maximum {
			maximum = value
		}
	}
	return fmt.Sprintf("L-%03d", maximum+1)
}

func nextEventID(events []model.Event) string {
	maximum := 0
	for _, event := range events {
		matches := eventIDPattern.FindStringSubmatch(event.ID)
		if len(matches) != 2 {
			continue
		}
		value, err := strconv.Atoi(matches[1])
		if err == nil && value > maximum {
			maximum = value
		}
	}
	return fmt.Sprintf("E-%03d", maximum+1)
}

func nextPromotionID(promotions []model.Promotion) string {
	maximum := 0
	for _, promotion := range promotions {
		matches := promotionIDPattern.FindStringSubmatch(promotion.ID)
		if len(matches) != 2 {
			continue
		}
		value, err := strconv.Atoi(matches[1])
		if err == nil && value > maximum {
			maximum = value
		}
	}
	return fmt.Sprintf("M-%03d", maximum+1)
}

func decisionLess(left, right string) bool {
	leftMatch := decisionIDPattern.FindStringSubmatch(left)
	rightMatch := decisionIDPattern.FindStringSubmatch(right)
	if len(leftMatch) == 2 && len(rightMatch) == 2 {
		leftNumber, _ := strconv.Atoi(leftMatch[1])
		rightNumber, _ := strconv.Atoi(rightMatch[1])
		return leftNumber < rightNumber
	}
	return left < right
}

func eventLess(left, right string) bool {
	leftMatch := eventIDPattern.FindStringSubmatch(left)
	rightMatch := eventIDPattern.FindStringSubmatch(right)
	if len(leftMatch) == 2 && len(rightMatch) == 2 {
		leftNumber, _ := strconv.Atoi(leftMatch[1])
		rightNumber, _ := strconv.Atoi(rightMatch[1])
		return leftNumber < rightNumber
	}
	return left < right
}

func proposalLess(left, right string) bool {
	leftMatch := proposalIDPattern.FindStringSubmatch(left)
	rightMatch := proposalIDPattern.FindStringSubmatch(right)
	if len(leftMatch) == 2 && len(rightMatch) == 2 {
		leftNumber, _ := strconv.Atoi(leftMatch[1])
		rightNumber, _ := strconv.Atoi(rightMatch[1])
		return leftNumber < rightNumber
	}
	return left < right
}

func promotionLess(left, right string) bool {
	leftMatch := promotionIDPattern.FindStringSubmatch(left)
	rightMatch := promotionIDPattern.FindStringSubmatch(right)
	if len(leftMatch) == 2 && len(rightMatch) == 2 {
		leftNumber, _ := strconv.Atoi(leftMatch[1])
		rightNumber, _ := strconv.Atoi(rightMatch[1])
		return leftNumber < rightNumber
	}
	return left < right
}

func validateEventHash(event model.Event) error {
	expected := event
	if err := expected.RefreshContentHash(); err != nil {
		return err
	}
	if expected.ContentHash != event.ContentHash {
		return fmt.Errorf("content_hash does not match content")
	}
	return nil
}

func validateProposalHash(proposal model.Proposal) error {
	expected := proposal
	if err := expected.RefreshContentHash(); err != nil {
		return err
	}
	if expected.ContentHash != proposal.ContentHash {
		return fmt.Errorf("content_hash does not match content")
	}
	return nil
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isYAML(name string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	return extension == ".yaml" || extension == ".yml"
}

func readYAML(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple YAML documents are not supported")
		}
		return err
	}
	return nil
}

func writeYAML(path string, value any) error {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".agc-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(buffer.Bytes()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
