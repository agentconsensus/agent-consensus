package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-consensus/ac/internal/api"
	"github.com/agent-consensus/ac/internal/model"
	"github.com/agent-consensus/ac/internal/server"
	"github.com/agent-consensus/ac/internal/serverclient"
)

func TestRepositoryDecisionsAndEventsRequireExplicitPromotion(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "server-state.json")
	service, err := server.Open(dataPath, "shared-secret")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	client, err := serverclient.New(httpServer.URL, "shared-secret")
	if err != nil {
		t.Fatal(err)
	}

	decision := sampleDecision(t, "D-001", "Payment API idempotency", []string{"backend"})
	event := sampleEvent(t, "E-001", "2026-08 outage", []string{"backend"})
	response, err := client.Sync(context.Background(), "payment-team", api.SyncRequest{Organization: "payment-team", Repository: "payments", Source: "test", Decisions: []model.Decision{decision}, Events: []model.Event{event}})
	if err != nil {
		t.Fatalf("sync first repository: %v", err)
	}
	if response.Applied != 2 || response.Organization.RuleCount != 0 {
		t.Fatalf("sync response = %#v", response)
	}

	snapshot, err := client.Snapshot(context.Background(), "payment-team", "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Decisions) != 1 || len(snapshot.Events) != 1 || len(snapshot.Rules) != 0 {
		t.Fatalf("unpromoted snapshot = %#v", snapshot)
	}

	promotion := samplePromotion(t, "M-001", decision)
	pending, err := client.SubmitPromotion(context.Background(), "payment-team", api.SubmitPromotionRequest{Repository: "payments", Source: "test", Promotion: promotion})
	if err != nil {
		t.Fatalf("submit promotion: %v", err)
	}
	if pending.ID != "M-001" || pending.Status != "pending" {
		t.Fatalf("pending promotion = %#v", pending)
	}
	reviewed := reviewPromotion(t, httpServer.URL, "shared-secret", "payment-team", pending.ID, api.ReviewPromotionRequest{Status: "approved", Title: "Idempotency is mandatory"})
	if reviewed.Status != "approved" || reviewed.RuleID != "R-001" {
		t.Fatalf("reviewed promotion = %#v", reviewed)
	}

	snapshot, err = client.Snapshot(context.Background(), "payment-team", "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Decisions) != 1 || snapshot.Decisions[0].ID != "D-001" || len(snapshot.Events) != 1 || len(snapshot.Rules) != 1 || snapshot.Rules[0].ID != "R-001" || len(snapshot.ResolvedPromotions) != 1 {
		t.Fatalf("promoted snapshot = %#v", snapshot)
	}

	other := sampleDecision(t, "D-001", "Ledger retention", []string{"backend"})
	if _, err := client.Sync(context.Background(), "payment-team", api.SyncRequest{Repository: "ledger", Source: "test", Decisions: []model.Decision{other}}); err != nil {
		t.Fatal(err)
	}
	otherSnapshot, err := client.Snapshot(context.Background(), "payment-team", "ledger")
	if err != nil {
		t.Fatal(err)
	}
	if len(otherSnapshot.Decisions) != 1 || otherSnapshot.Decisions[0].Title != "Ledger retention" || len(otherSnapshot.Events) != 0 || len(otherSnapshot.Rules) != 1 {
		t.Fatalf("second repository snapshot = %#v", otherSnapshot)
	}

	detail, err := getDetail(httpServer.URL, "shared-secret", "payment-team")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Organization.DecisionCount != 2 || detail.Organization.EventCount != 1 || detail.Organization.RuleCount != 1 || len(detail.Decisions) != 2 || len(detail.Events) != 1 || len(detail.Promotions) != 1 {
		t.Fatalf("organization detail = %#v", detail)
	}
}

func TestPendingDecisionProposalIsNotPulledByAnyRepository(t *testing.T) {
	service, err := server.Open(filepath.Join(t.TempDir(), "server-state.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	client, err := serverclient.New(httpServer.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	// Sync creates the organization and source repository without creating a decision.
	if _, err := client.Sync(context.Background(), "payment-team", api.SyncRequest{Repository: "payments"}); err != nil {
		t.Fatal(err)
	}
	pending, err := client.SubmitProposal(context.Background(), "payment-team", api.SubmitProposalRequest{Repository: "payments", Proposal: sampleProposal(t, "L-001", "Temporary retry rule", []string{"backend"})})
	if err != nil {
		t.Fatal(err)
	}
	if pending.ID != "P-001" || pending.Status != "pending" {
		t.Fatalf("pending proposal = %#v", pending)
	}
	for _, repository := range []string{"payments", "ledger"} {
		snapshot, err := client.Snapshot(context.Background(), "payment-team", repository)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Decisions) != 0 || len(snapshot.ResolvedProposals) != 0 {
			t.Fatalf("pending proposal leaked to %s: %#v", repository, snapshot)
		}
	}
	rejected := reviewProposal(t, httpServer.URL, "", "payment-team", pending.ID, api.ReviewProposalRequest{Status: "rejected"})
	if rejected.Status != "rejected" || rejected.DecisionID != "" {
		t.Fatalf("rejected proposal = %#v", rejected)
	}
	sourceSnapshot, err := client.Snapshot(context.Background(), "payment-team", "payments")
	if err != nil {
		t.Fatal(err)
	}
	otherSnapshot, err := client.Snapshot(context.Background(), "payment-team", "ledger")
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceSnapshot.ResolvedProposals) != 1 || len(otherSnapshot.ResolvedProposals) != 0 {
		t.Fatalf("terminal proposal visibility source=%#v other=%#v", sourceSnapshot, otherSnapshot)
	}
}

func sampleDecision(t *testing.T, id, title string, scope []string) model.Decision {
	t.Helper()
	uid, err := model.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	decision := model.Decision{SchemaVersion: model.CurrentSchemaVersion, ID: id, UID: uid, Revision: 1, Title: title, Statement: title + " statement", Scope: scope, Owner: "architecture", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := decision.RefreshContentHash(); err != nil {
		t.Fatal(err)
	}
	return decision
}
func sampleEvent(t *testing.T, id, title string, scope []string) model.Event {
	t.Helper()
	uid, err := model.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event := model.Event{SchemaVersion: model.CurrentSchemaVersion, ID: id, UID: uid, Title: title, Statement: title + " case", Scope: scope, CreatedAt: now, UpdatedAt: now}
	if err := event.RefreshContentHash(); err != nil {
		t.Fatal(err)
	}
	return event
}
func samplePromotion(t *testing.T, id string, decision model.Decision) model.Promotion {
	t.Helper()
	uid, err := model.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return model.Promotion{SchemaVersion: model.CurrentSchemaVersion, ID: id, UID: uid, DecisionID: decision.ID, DecisionUID: decision.UID, DecisionHash: decision.ContentHash, Status: "local", CreatedAt: now, UpdatedAt: now}
}
func sampleProposal(t *testing.T, id, title string, scope []string) model.Proposal {
	t.Helper()
	uid, err := model.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	proposal := model.Proposal{SchemaVersion: model.CurrentSchemaVersion, ID: id, UID: uid, Kind: "decision", Title: title, Statement: title + " statement", Scope: scope, Owner: "architecture", Status: "local", CreatedAt: now, UpdatedAt: now}
	if err := proposal.RefreshContentHash(); err != nil {
		t.Fatal(err)
	}
	return proposal
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
		t.Fatalf("review response = %s", response.Status)
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
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("review response = %s", response.Status)
	}
	var proposal model.Proposal
	if err := json.NewDecoder(response.Body).Decode(&proposal); err != nil {
		t.Fatal(err)
	}
	return proposal
}
func getDetail(baseURL, token, organization string) (api.OrganizationDetail, error) {
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/organizations/"+organization, nil)
	if err != nil {
		return api.OrganizationDetail{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return api.OrganizationDetail{}, err
	}
	defer response.Body.Close()
	var detail api.OrganizationDetail
	err = json.NewDecoder(response.Body).Decode(&detail)
	return detail, err
}

func TestContextEndpointRendersMarkdown(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "server-state.json")
	service, err := server.Open(dataPath, "shared-secret")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	client, err := serverclient.New(httpServer.URL, "shared-secret")
	if err != nil {
		t.Fatal(err)
	}

	decision := sampleDecision(t, "D-001", "Payment API idempotency", []string{"backend"})
	if _, err := client.Sync(context.Background(), "payment-team", api.SyncRequest{Organization: "payment-team", Repository: "payments", Source: "test", Decisions: []model.Decision{decision}}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/organizations/payment-team/context?repository=payments&format=markdown", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer shared-secret")
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("markdown context status = %s", response.Status)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/markdown") {
		t.Fatalf("markdown content-type = %q", contentType)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"# Agent Context", "Repository: payments", "D-001"} {
		if !strings.Contains(string(body), fragment) {
			t.Errorf("markdown is missing %q:\n%s", fragment, body)
		}
	}
}

func TestDeleteRepositoryDecision(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "server-state.json")
	service, err := server.Open(dataPath, "shared-secret")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	client, err := serverclient.New(httpServer.URL, "shared-secret")
	if err != nil {
		t.Fatal(err)
	}

	decision := sampleDecision(t, "D-001", "Payment API idempotency", []string{"backend"})
	if _, err := client.Sync(context.Background(), "payment-team", api.SyncRequest{Organization: "payment-team", Repository: "payments", Source: "test", Decisions: []model.Decision{decision}}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	deleteRequest := func() *http.Response {
		request, err := http.NewRequest(http.MethodDelete, httpServer.URL+"/api/v1/organizations/payment-team/repositories/payments/decisions/"+decision.UID, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer shared-secret")
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	response := deleteRequest()
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %s", response.Status)
	}
	detail, err := getDetail(httpServer.URL, "shared-secret", "payment-team")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Decisions) != 0 || detail.Organization.DecisionCount != 0 {
		t.Fatalf("decision still present after delete: %#v", detail.Decisions)
	}

	response = deleteRequest()
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %s", response.Status)
	}
}
