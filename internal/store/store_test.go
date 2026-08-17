package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-consensus/ac/internal/model"
	"github.com/agent-consensus/ac/internal/store"
)

func TestInitializeAndCreateDecisions(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), store.DirectoryName)
	state, err := store.Initialize(root, model.Config{
		SchemaVersion: model.CurrentSchemaVersion,
		Organization:  "payment-team",
		Repository:    "payment-service",
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("initialize state: %v", err)
	}
	for _, directory := range []string{state.LocalDir(), state.DecisionsDir(), state.RulesDir(), state.EventsDir(), state.PromotionsDir(), state.KnowledgeDir(), state.SessionsDir(), state.ContextDir()} {
		if info, statErr := filepath.Glob(directory); statErr != nil || len(info) != 1 {
			t.Fatalf("expected directory %s to exist, matches=%v err=%v", directory, info, statErr)
		}
	}

	first, err := state.CreateDecision(
		"Payment API idempotency",
		"All payment operations must support an idempotency key.",
		[]string{" Backend ", "frontend", "backend"},
		"architecture-team",
	)
	if err != nil {
		t.Fatalf("create first decision: %v", err)
	}
	if first.ID != "D-001" {
		t.Errorf("first decision ID = %s, want D-001", first.ID)
	}
	if got, want := len(first.Scope), 2; got != want || first.Scope[0] != "backend" || first.Scope[1] != "frontend" {
		t.Errorf("normalized scope = %#v, want [backend frontend]", first.Scope)
	}
	if first.UID == "" || first.ContentHash == "" || first.Revision != 1 {
		t.Errorf("new decision missing sync metadata: %#v", first)
	}

	second, err := state.CreateDecision("Decimal money", "All money calculations use Decimal.", []string{"all"}, "architecture-team")
	if err != nil {
		t.Fatalf("create second decision: %v", err)
	}
	if second.ID != "D-002" {
		t.Errorf("second decision ID = %s, want D-002", second.ID)
	}

	decisions, err := state.ListDecisions()
	if err != nil {
		t.Fatalf("list decisions: %v", err)
	}
	if len(decisions) != 2 || decisions[0].ID != "D-001" || decisions[1].ID != "D-002" {
		t.Errorf("listed decisions = %#v, want D-001 then D-002", decisions)
	}

	if _, err := store.Initialize(root, model.Config{SchemaVersion: model.CurrentSchemaVersion, Organization: "other", Repository: "other"}); err == nil {
		t.Error("second initialization unexpectedly succeeded")
	}
}

func TestEnsureLayoutAndPromotionLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), store.DirectoryName)
	state, err := store.Initialize(root, model.Config{
		SchemaVersion: model.CurrentSchemaVersion,
		Organization:  "payment-team",
		Repository:    "payment-service",
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("initialize state: %v", err)
	}
	if err := os.RemoveAll(state.EventsDir()); err != nil {
		t.Fatalf("remove events directory: %v", err)
	}
	if err := os.Remove(state.LocalGitIgnorePath()); err != nil {
		t.Fatalf("remove nested gitignore: %v", err)
	}
	if err := state.EnsureLayout(); err != nil {
		t.Fatalf("upgrade local layout: %v", err)
	}
	ignored, err := os.ReadFile(state.LocalGitIgnorePath())
	if err != nil {
		t.Fatalf("read nested gitignore: %v", err)
	}
	if !strings.Contains(string(ignored), "credentials.yaml") {
		t.Errorf("nested gitignore = %q", ignored)
	}

	decision, err := state.CreateDecision("Payment API idempotency", "All writes require an idempotency key.", []string{"backend", "backend"}, "architecture")
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	event, err := state.CreateEvent("Retry incident", "A retry doubled a request.", []string{"backend"})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if event.ID != "E-001" || event.ContentHash == "" {
		t.Errorf("created event = %#v", event)
	}
	promotion, err := state.CreatePromotion(decision.ID)
	if err != nil {
		t.Fatalf("create promotion: %v", err)
	}
	if promotion.ID != "M-001" || promotion.Status != "local" || promotion.DecisionUID != decision.UID {
		t.Errorf("created promotion = %#v", promotion)
	}

	remote := promotion
	remote.ID = "M-001"
	remote.Status = "pending"
	remote.Repository = "payment-service"
	remote.SubmittedAt = time.Now().UTC()
	if _, err := state.MarkPromotionSubmitted(promotion.UID, remote); err != nil {
		t.Fatalf("mark promotion submitted: %v", err)
	}
	promotions, err := state.ListPromotions()
	if err != nil {
		t.Fatalf("list promotions: %v", err)
	}
	if len(promotions) != 1 || promotions[0].Status != "pending" || promotions[0].RemoteID != "M-001" {
		t.Errorf("submitted promotions = %#v", promotions)
	}

	remote.Status = "approved"
	remote.RuleID = "R-001"
	remote.ReviewNote = "Useful beyond this repo."
	remote.UpdatedAt = time.Now().UTC()
	if updated, err := state.ApplyResolvedPromotions([]model.Promotion{remote}); err != nil || updated != 1 {
		t.Errorf("apply promotion = %d, %v", updated, err)
	}
	promotions, err = state.ListPromotions()
	if err != nil {
		t.Fatal(err)
	}
	if promotions[0].Status != "approved" || promotions[0].RuleID != "R-001" {
		t.Errorf("resolved promotions = %#v", promotions)
	}
	decisions, err := state.ListDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].ID != decision.ID {
		t.Errorf("promotion removed decision: %#v", decisions)
	}
	if _, err := state.CreatePromotion(decision.ID); err != nil {
		// A subsequent explicit promotion is allowed only after a terminal status.
		t.Errorf("create later promotion: %v", err)
	}
}

func TestFindFromNestedDirectory(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	root := filepath.Join(workspace, store.DirectoryName)
	if _, err := store.Initialize(root, model.Config{SchemaVersion: model.CurrentSchemaVersion, Organization: "team", Repository: "repo"}); err != nil {
		t.Fatalf("initialize state: %v", err)
	}
	nested := filepath.Join(workspace, "services", "payments")
	if err := mkdirAll(nested); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	state, err := store.Find(nested)
	if err != nil {
		t.Fatalf("find state from nested directory: %v", err)
	}
	if state.Root != root {
		t.Errorf("found root = %s, want %s", state.Root, root)
	}
}
