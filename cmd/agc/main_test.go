package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-consensus/ac/internal/api"
	"github.com/agent-consensus/ac/internal/model"
	"github.com/agent-consensus/ac/internal/server"
	"github.com/agent-consensus/ac/internal/store"
)

func TestRepositoryDecisionAndEventWorkflow(t *testing.T) {
	workingDirectory := t.TempDir()
	var output, errorOutput bytes.Buffer
	if err := run([]string{"init", "--org", "payment-team", "--repo", "payment-service", "--no-remote"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"decision", "create", "--title", "Payment API idempotency", "--statement", "All writes require a key.", "--scope", "backend", "--owner", "architecture"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Created temporary local decision L-001") {
		t.Fatalf("decision output = %s", output.String())
	}
	output.Reset()
	if err := run([]string{"event", "create", "--title", "Refund incident", "--statement", "The retry path doubled a refund.", "--scope", "backend"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := state.ListDecisions()
	if err != nil {
		t.Fatal(err)
	}
	events, err := state.ListEvents()
	if err != nil {
		t.Fatal(err)
	}
	localDecisions, err := state.ListProposals()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 || len(localDecisions) != 1 || len(events) != 1 {
		t.Fatalf("repository decisions=%#v local decisions=%#v events=%#v", decisions, localDecisions, events)
	}
	output.Reset()
	if err := run([]string{"context", "--role", "backend", "--agent", "codex"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"L-001", "E-001", "## Organization Rules", "## Known Cases"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("context missing %q:\n%s", expected, output.String())
		}
	}
}

func TestExplicitPromotionCreatesRuleButKeepsRepositoryDecision(t *testing.T) {
	workingDirectory := t.TempDir()
	service, err := server.Open(filepath.Join(t.TempDir(), "server-state.json"), "test-token")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	var output, errorOutput bytes.Buffer
	if err := run([]string{"init", "--org", "payment-team", "--repo", "payment-service", "--server", httpServer.URL, "--token", "test-token"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"decision", "create", "--title", "Money uses Decimal", "--statement", "All money calculations use Decimal.", "--scope", "backend", "--owner", "architecture"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"push"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1 temporary decision(s) submitted") {
		t.Fatalf("push output = %s", output.String())
	}
	reviewedProposal := reviewProposal(t, httpServer.URL, "test-token", "payment-team", "P-001", api.ReviewProposalRequest{Status: "approved"})
	if reviewedProposal.DecisionID != "D-001" {
		t.Fatalf("reviewed proposal = %#v", reviewedProposal)
	}
	if err := run([]string{"pull"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"decision", "promote", "D-001"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Submitted D-001 as M-001") {
		t.Fatalf("promotion output = %s", output.String())
	}
	reviewed := reviewPromotion(t, httpServer.URL, "test-token", "payment-team", "M-001", api.ReviewPromotionRequest{Status: "approved", Title: "Decimal money"})
	if reviewed.RuleID != "R-001" {
		t.Fatalf("reviewed promotion = %#v", reviewed)
	}
	output.Reset()
	if err := run([]string{"pull"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := state.ListDecisions()
	if err != nil {
		t.Fatal(err)
	}
	rules, err := state.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	promotions, err := state.ListPromotions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].ID != "D-001" || len(rules) != 1 || rules[0].ID != "R-001" || len(promotions) != 1 || promotions[0].Status != "approved" {
		t.Fatalf("decision=%#v rules=%#v promotions=%#v", decisions, rules, promotions)
	}
	output.Reset()
	errorOutput.Reset()
	if err := run([]string{"context", "--role", "backend", "--agent", "codex", "--record"}, workingDirectory, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errorOutput.String(), "Recorded context CR-000001") {
		t.Fatalf("record output = %s", errorOutput.String())
	}
}

func TestStatusWithoutInitialization(t *testing.T) {
	var output, errorOutput bytes.Buffer
	if err := run([]string{"status"}, t.TempDir(), &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Not initialized. Run agc init.") {
		t.Fatalf("status output = %s", output.String())
	}
}

func reviewPromotion(t *testing.T, baseURL, token, organization, id string, input api.ReviewPromotionRequest) model.Promotion {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPatch, baseURL+"/api/v1/organizations/"+organization+"/promotions/"+id, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("review status = %s", response.Status)
	}
	var promotion model.Promotion
	if err := json.NewDecoder(response.Body).Decode(&promotion); err != nil {
		t.Fatal(err)
	}
	return promotion
}

func reviewProposal(t *testing.T, baseURL, token, organization, id string, input api.ReviewProposalRequest) model.Proposal {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPatch, baseURL+"/api/v1/organizations/"+organization+"/proposals/"+id, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proposal review status = %s", response.Status)
	}
	var proposal model.Proposal
	if err := json.NewDecoder(response.Body).Decode(&proposal); err != nil {
		t.Fatal(err)
	}
	return proposal
}
