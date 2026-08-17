package context_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentcontext "github.com/agent-consensus/ac/internal/context"
	"github.com/agent-consensus/ac/internal/model"
	"github.com/agent-consensus/ac/internal/store"
)

func TestBuildFiltersConsensusByRole(t *testing.T) {
	t.Parallel()
	state, err := store.Initialize(filepath.Join(t.TempDir(), store.DirectoryName), model.Config{
		SchemaVersion: model.CurrentSchemaVersion,
		Organization:  "payment-team",
		Repository:    "payment-service",
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("initialize state: %v", err)
	}
	if _, err := state.CreateDecision("Payment API idempotency", "All payment operations must support an idempotency key.", []string{"backend"}, "architecture"); err != nil {
		t.Fatalf("create backend decision: %v", err)
	}
	if _, err := state.CreateDecision("Decimal money", "All money calculations use Decimal.", []string{"all"}, "architecture"); err != nil {
		t.Fatalf("create global decision: %v", err)
	}

	frontend, err := agentcontext.Build(state, "frontend", "codex")
	if err != nil {
		t.Fatalf("build frontend context: %v", err)
	}
	if len(frontend.Decisions) != 1 || frontend.Decisions[0].Title != "Decimal money" {
		t.Errorf("frontend decisions = %#v, want only global decision", frontend.Decisions)
	}
	if frontend.ContextHash == "" {
		t.Error("context hash is empty")
	}

	backend, err := agentcontext.Build(state, "backend", "codex")
	if err != nil {
		t.Fatalf("build backend context: %v", err)
	}
	if len(backend.Decisions) != 2 {
		t.Errorf("backend decision count = %d, want 2", len(backend.Decisions))
	}
	organizationWide, err := agentcontext.Build(state, "", "codex")
	if err != nil {
		t.Fatalf("build organization-wide context: %v", err)
	}
	if len(organizationWide.Decisions) != 2 {
		t.Errorf("organization-wide decision count = %d, want 2", len(organizationWide.Decisions))
	}
	markdown := agentcontext.Markdown(backend)
	for _, text := range []string{"# Agent Context", "D-001", "D-002", "Current Role: backend"} {
		if !strings.Contains(markdown, text) {
			t.Errorf("markdown is missing %q:\n%s", text, markdown)
		}
	}
}
