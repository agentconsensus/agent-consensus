// agc is the local-first CLI for agent organization consensus.
package main

import (
	"bufio"
	stdcontext "context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-consensus/ac/internal/api"
	agentcontext "github.com/agent-consensus/ac/internal/context"
	"github.com/agent-consensus/ac/internal/model"
	"github.com/agent-consensus/ac/internal/server"
	"github.com/agent-consensus/ac/internal/serverclient"
	"github.com/agent-consensus/ac/internal/store"
)

const version = "0.4.0-dev"

func main() {
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agc: determine working directory:", err)
		os.Exit(1)
	}
	if err := runWithInput(os.Args[1:], workingDirectory, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agc:", err)
		os.Exit(1)
	}
}

func run(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	// Tests and embedders use run directly; keep that compatibility path
	// deliberately non-interactive. The executable calls runWithInput with
	// os.Stdin and therefore retains the guided init/login flow.
	return runWithInput(args, workingDirectory, strings.NewReader(""), output, errorOutput)
}

func runWithInput(args []string, workingDirectory string, input io.Reader, output, errorOutput io.Writer) error {
	if len(args) == 0 {
		printUsage(output)
		return nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		printUsage(output)
		return nil
	case "version", "--version", "-v":
		fmt.Fprintln(output, version)
		return nil
	case "init":
		return runInit(args[1:], workingDirectory, input, output, errorOutput)
	case "status":
		return runStatus(args[1:], workingDirectory, output, errorOutput)
	case "login":
		return runLogin(args[1:], workingDirectory, input, output, errorOutput)
	case "pull":
		return runPull(args[1:], workingDirectory, output, errorOutput)
	case "push":
		return runPush(args[1:], workingDirectory, output, errorOutput)
	case "context":
		return runContext(args[1:], workingDirectory, output, errorOutput)
	case "decision":
		return runDecision(args[1:], workingDirectory, output, errorOutput)
	case "event":
		return runEvent(args[1:], workingDirectory, output, errorOutput)
	case "promotion":
		return runPromotion(args[1:], workingDirectory, output, errorOutput)
	case "sync":
		return runSync(args[1:], workingDirectory, output, errorOutput)
	case "server":
		return runServer(args[1:], workingDirectory, output, errorOutput)
	default:
		return fmt.Errorf("unknown command %q; run agc help", args[0])
	}
}

// detectGitRemoteURL best-effort reads the origin remote of the git
// repository containing the working directory. Any failure (no git binary,
// not a repository, no origin remote) simply yields no link.
func detectGitRemoteURL(workingDirectory string) string {
	remote, err := exec.Command("git", "-C", workingDirectory, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(remote))
}

func runInit(args []string, workingDirectory string, input io.Reader, output, errorOutput io.Writer) error {
	flags := newFlagSet("init", errorOutput)
	organization := flags.String("org", "", "organization name")
	repository := flags.String("repo", "", "repository name (defaults to the current directory)")
	serverURL := flags.String("server", "", "existing agc server URL")
	token := flags.String("token", os.Getenv("AGC_ACCESS_TOKEN"), "agc server access token")
	noRemote := flags.Bool("no-remote", false, "skip remote server setup and pull")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init does not accept positional arguments")
	}
	tokenProvided := flagProvided(flags, "token")
	prompts := newPromptSession(input, output)
	stateRoot := filepath.Join(workingDirectory, store.DirectoryName)
	info, err := os.Stat(stateRoot)
	var state store.State
	switch {
	case err == nil && info.IsDir():
		state = store.New(stateRoot)
		if err := state.EnsureLayout(); err != nil {
			return err
		}
	case err == nil:
		return fmt.Errorf("%s exists but is not a directory", stateRoot)
	case errors.Is(err, os.ErrNotExist):
		organizationName := strings.TrimSpace(*organization)
		if organizationName == "" {
			organizationName = filepath.Base(workingDirectory)
			if prompts.Enabled() {
				organizationName, err = prompts.String("Organization name", organizationName)
				if err != nil {
					return err
				}
			}
		}
		repositoryName := strings.TrimSpace(*repository)
		if repositoryName == "" {
			repositoryName = filepath.Base(workingDirectory)
		}
		state, err = store.Initialize(stateRoot, model.Config{
			SchemaVersion: model.CurrentSchemaVersion,
			Organization:  organizationName,
			Repository:    repositoryName,
			CreatedAt:     time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Initialized .agc for %s / %s\n", organizationName, repositoryName)
	default:
		return fmt.Errorf("inspect .agc: %w", err)
	}

	config, err := state.LoadConfig()
	if err != nil {
		return err
	}
	if remoteURL := detectGitRemoteURL(workingDirectory); remoteURL != "" && remoteURL != config.RepositoryURL {
		config, err = state.SaveRepositoryURL(remoteURL)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Detected git remote: %s\n", remoteURL)
	}
	shouldPull := false
	if *noRemote {
		// Explicitly keep existing remote configuration untouched while skipping
		// the optional initialization pull.
	} else if strings.TrimSpace(*serverURL) != "" {
		accessToken, credentialErr := preferredAccessToken(state, *token, tokenProvided)
		if credentialErr != nil {
			return credentialErr
		}
		if err := saveRemoteConnection(state, *serverURL, accessToken); err != nil {
			return err
		}
		config, err = state.LoadConfig()
		if err != nil {
			return err
		}
		shouldPull = true
	} else if prompts.Enabled() && strings.TrimSpace(config.ServerURL) == "" {
		useRemote, promptErr := prompts.YesNo("Connect to an existing AGC Server", false)
		if promptErr != nil {
			return promptErr
		}
		if useRemote {
			server, promptErr := prompts.String("AGC Server URL", "")
			if promptErr != nil {
				return promptErr
			}
			accessToken, promptErr := prompts.String("Access token (leave blank when not required)", "")
			if promptErr != nil {
				return promptErr
			}
			if err := saveRemoteConnection(state, server, accessToken); err != nil {
				return err
			}
			config, err = state.LoadConfig()
			if err != nil {
				return err
			}
			shouldPull = true
		}
	} else if strings.TrimSpace(config.ServerURL) != "" {
		shouldPull = true
	}

	if shouldPull {
		result, snapshot, pullErr := pullState(state, "", "")
		switch {
		case pullErr == nil:
			fmt.Fprintf(output, "Pulled %s / %s: %d applied, %d unchanged", snapshot.Organization.Name, config.Repository, result.Applied, result.Unchanged)
			if result.ResolvedProposals > 0 {
				fmt.Fprintf(output, ", %d local decision(s) reviewed", result.ResolvedProposals)
			}
			if result.ResolvedPromotions > 0 {
				fmt.Fprintf(output, ", %d promotion(s) resolved", result.ResolvedPromotions)
			}
			fmt.Fprintln(output)
		case serverclient.IsNotFound(pullErr):
			fmt.Fprintln(output, "No matching remote organization found; initialized local .agc only.")
		default:
			fmt.Fprintf(errorOutput, "Remote pull skipped: %v\n", pullErr)
		}
	}

	if err := writeStatus(state, output); err != nil {
		return err
	}
	fmt.Fprintln(output, "\nAGC initialization complete.")
	return nil
}

func runStatus(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("status", errorOutput)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("status does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		if strings.Contains(err.Error(), "no "+store.DirectoryName+" directory") {
			fmt.Fprintln(output, "AGC Status\n\nNot initialized. Run agc init.")
			return nil
		}
		return err
	}
	return writeStatus(state, output)
}

func runLogin(args []string, workingDirectory string, input io.Reader, output, errorOutput io.Writer) error {
	flags := newFlagSet("login", errorOutput)
	serverURL := flags.String("server", "", "agc server URL")
	defaultToken := os.Getenv("AGC_ACCESS_TOKEN")
	token := flags.String("token", defaultToken, "agc server access token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("login does not accept positional arguments")
	}
	tokenProvided := flagProvided(flags, "token")
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	config, err := state.LoadConfig()
	if err != nil {
		return err
	}
	prompts := newPromptSession(input, output)
	server := strings.TrimSpace(*serverURL)
	if server == "" {
		if !prompts.Enabled() {
			return errors.New("--server is required when login is not interactive")
		}
		server, err = prompts.String("AGC Server URL", config.ServerURL)
		if err != nil {
			return err
		}
	}
	accessToken, err := preferredAccessToken(state, *token, tokenProvided)
	if err != nil {
		return err
	}
	if !tokenProvided && *token == "" && prompts.Enabled() {
		accessToken, err = prompts.String("Access token (leave blank when not required)", "")
		if err != nil {
			return err
		}
	}
	if err := saveRemoteConnection(state, server, accessToken); err != nil {
		return err
	}
	fmt.Fprintf(output, "Configured AGC Server: %s\n", strings.TrimRight(strings.TrimSpace(server), "/"))
	return nil
}

func saveRemoteConnection(state store.State, serverURL, accessToken string) error {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		return errors.New("AGC Server URL is required")
	}
	if _, err := serverclient.New(serverURL, accessToken); err != nil {
		return err
	}
	if _, err := state.SaveConnection(serverURL, accessToken); err != nil {
		return err
	}
	return nil
}

// preferredAccessToken protects an existing local credential when a user only
// changes the server URL. Passing --token explicitly (including an empty
// value) remains an intentional replacement; AGC_ACCESS_TOKEN also wins.
func preferredAccessToken(state store.State, provided string, explicitlyProvided bool) (string, error) {
	provided = strings.TrimSpace(provided)
	if explicitlyProvided || provided != "" {
		return provided, nil
	}
	credentials, err := state.LoadCredentials()
	if err != nil {
		return "", err
	}
	return credentials.AccessToken, nil
}

func writeStatus(state store.State, output io.Writer) error {
	config, err := state.LoadConfig()
	if err != nil {
		return err
	}
	decisions, err := state.ListDecisions()
	if err != nil {
		return err
	}
	rules, err := state.ListRules()
	if err != nil {
		return err
	}
	events, err := state.ListEvents()
	if err != nil {
		return err
	}
	localDecisions, err := state.ListProposals()
	if err != nil {
		return err
	}
	promotions, err := state.ListPromotions()
	if err != nil {
		return err
	}
	active := make([]model.Decision, 0)
	for _, decision := range decisions {
		if strings.EqualFold(decision.Status, "active") {
			active = append(active, decision)
		}
	}
	localPromotions := 0
	submittedPromotions := 0
	for _, promotion := range promotions {
		switch promotion.Status {
		case "local":
			localPromotions++
		case "submitted":
			submittedPromotions++
		}
	}

	fmt.Fprintln(output, "AGC Status")
	fmt.Fprintln(output)
	fmt.Fprintf(output, "Organization: %s\n", config.Organization)
	fmt.Fprintf(output, "Repository: %s\n", config.Repository)
	if config.ServerURL == "" {
		fmt.Fprintln(output, "Remote: not configured")
	} else {
		fmt.Fprintf(output, "Remote: %s\n", config.ServerURL)
	}
	fmt.Fprintln(output, "\nActive Decisions:")
	if len(active) == 0 {
		fmt.Fprintln(output, "  None.")
	} else {
		for _, decision := range active {
			fmt.Fprintf(output, "  %s  %s\n", decision.ID, decision.Title)
		}
	}
	fmt.Fprintln(output, "\nOrganization Rules:")
	if len(rules) == 0 {
		fmt.Fprintln(output, "  None.")
	} else {
		for _, rule := range rules {
			fmt.Fprintf(output, "  %s  %s (%s)\n", rule.ID, rule.Title, rule.Status)
		}
	}
	fmt.Fprintf(output, "\nRepository Events: %d\n", len(events))
	fmt.Fprintf(output, "\nTemporary Local Decisions: %d\n", len(localDecisions))
	fmt.Fprintf(output, "\nPromotions:\n  %d local, %d submitted for review.\n", localPromotions, submittedPromotions)
	return nil
}

func runDecision(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing decision subcommand; use create, list, or promote")
	}
	switch args[0] {
	case "create":
		return runDecisionCreate(args[1:], workingDirectory, output, errorOutput)
	case "list":
		return runDecisionList(args[1:], workingDirectory, output, errorOutput)
	case "promote":
		return runDecisionPromote(args[1:], workingDirectory, output, errorOutput)
	default:
		return fmt.Errorf("unknown decision subcommand %q; use create, list, or promote", args[0])
	}
}

func runDecisionCreate(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("decision create", errorOutput)
	title := flags.String("title", "", "decision title (required)")
	statement := flags.String("statement", "", "decision statement (required)")
	owner := flags.String("owner", "", "decision owner")
	scope := flags.String("scope", "", "comma-separated roles, or all (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("decision create does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	decision, err := state.CreateProposal(*title, *statement, strings.Split(*scope, ","), *owner)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Created temporary local decision %s: %s\n", decision.ID, decision.Title)
	fmt.Fprintf(output, "File: %s\n", filepath.Join(state.LocalDir(), decision.ID+".yaml"))
	fmt.Fprintln(output, "Run agc push to submit it for repository review. Only approval creates a repository D-### decision.")
	return nil
}

func runDecisionPromote(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("decision promote", errorOutput)
	serverURL := flags.String("server", "", "agc server URL override")
	token := flags.String("token", "", "agc server access token override")
	source := flags.String("source", "agc CLI", "promotion source label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("decision promote requires one decision ID")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	config, client, err := resolveRemote(state, *serverURL, *token)
	if err != nil {
		return err
	}
	promotion, err := state.CreatePromotion(flags.Arg(0))
	if err != nil {
		return err
	}
	// A promotion always syncs the decision first. This makes the server's
	// review target explicit and prevents accidentally promoting stale content.
	if _, err := syncRepository(state, config, client, *source); err != nil {
		return err
	}
	remote, err := client.SubmitPromotion(stdcontext.Background(), config.Organization, api.SubmitPromotionRequest{
		Repository: config.Repository, Source: strings.TrimSpace(*source), Promotion: promotion,
	})
	if err != nil {
		return fmt.Errorf("submit promotion %s: %w", promotion.ID, err)
	}
	if _, err := state.MarkPromotionSubmitted(promotion.UID, remote); err != nil {
		return fmt.Errorf("promotion accepted by server but could not update local state: %w", err)
	}
	fmt.Fprintf(output, "Submitted %s as %s for organization-rule review\n", promotion.DecisionID, remote.ID)
	return nil
}

func runDecisionList(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("decision list", errorOutput)
	status := flags.String("status", "", "filter by status")
	format := flags.String("format", "table", "output format: table or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("decision list does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	decisions, err := state.ListDecisions()
	if err != nil {
		return err
	}
	if strings.TrimSpace(*status) != "" {
		filtered := make([]model.Decision, 0, len(decisions))
		for _, decision := range decisions {
			if strings.EqualFold(decision.Status, strings.TrimSpace(*status)) {
				filtered = append(filtered, decision)
			}
		}
		decisions = filtered
	}
	switch *format {
	case "table":
		if len(decisions) == 0 {
			fmt.Fprintln(output, "No decisions.")
			return nil
		}
		for _, decision := range decisions {
			fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", decision.ID, decision.Status, strings.Join(decision.Scope, ","), decision.Title)
		}
	case "json":
		return writeJSON(output, decisions)
	default:
		return fmt.Errorf("unsupported format %q; use table or json", *format)
	}
	return nil
}

func runEvent(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing event subcommand; use create or list")
	}
	switch args[0] {
	case "create":
		return runEventCreate(args[1:], workingDirectory, output, errorOutput)
	case "list":
		return runEventList(args[1:], workingDirectory, output, errorOutput)
	default:
		return fmt.Errorf("unknown event subcommand %q; use create or list", args[0])
	}
}

func runEventCreate(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("event create", errorOutput)
	title := flags.String("title", "", "event title (required)")
	statement := flags.String("statement", "", "factual case record (required)")
	scope := flags.String("scope", "all", "comma-separated roles, or all")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("event create does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	event, err := state.CreateEvent(*title, *statement, strings.Split(*scope, ","))
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Created repository event %s: %s\n", event.ID, event.Title)
	fmt.Fprintf(output, "File: %s\n", filepath.Join(state.EventsDir(), event.ID+".yaml"))
	return nil
}

func runEventList(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("event list", errorOutput)
	format := flags.String("format", "table", "output format: table or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("event list does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	events, err := state.ListEvents()
	if err != nil {
		return err
	}
	switch *format {
	case "table":
		if len(events) == 0 {
			fmt.Fprintln(output, "No events.")
			return nil
		}
		for _, event := range events {
			fmt.Fprintf(output, "%s\t%s\t%s\n", event.ID, strings.Join(event.Scope, ","), event.Title)
		}
	case "json":
		return writeJSON(output, events)
	default:
		return fmt.Errorf("unsupported format %q; use table or json", *format)
	}
	return nil
}

func runPromotion(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("missing promotion subcommand; use list")
	}
	flags := newFlagSet("promotion list", errorOutput)
	format := flags.String("format", "table", "output format: table or json")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("promotion list does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	promotions, err := state.ListPromotions()
	if err != nil {
		return err
	}
	switch *format {
	case "table":
		if len(promotions) == 0 {
			fmt.Fprintln(output, "No promotions.")
			return nil
		}
		for _, promotion := range promotions {
			fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", promotion.ID, promotion.Status, promotion.DecisionID, promotion.RuleID)
		}
	case "json":
		return writeJSON(output, promotions)
	default:
		return fmt.Errorf("unsupported format %q; use table or json", *format)
	}
	return nil
}

func runContext(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("context", errorOutput)
	role := flags.String("role", "", "agent role used to filter consensus")
	agent := flags.String("agent", "", "agent runtime name")
	format := flags.String("format", "markdown", "output format: markdown or json")
	record := flags.Bool("record", false, "record this delivered context with agc server")
	serverURL := flags.String("server", "", "agc server URL override (used with --record)")
	serverToken := flags.String("token", "", "agc server access token override")
	summary := flags.String("summary", "", "optional session summary for the context record")
	sessionID := flags.String("session", "", "optional session identifier for the context record")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("context does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	document, err := agentcontext.Build(state, *role, *agent)
	if err != nil {
		return err
	}
	if *record {
		if strings.TrimSpace(*agent) == "" {
			return errors.New("--agent is required with context --record")
		}
		config, client, err := resolveRemote(state, *serverURL, *serverToken)
		if err != nil {
			return err
		}
		decisionIDs := make([]string, 0, len(document.Decisions))
		for _, decision := range document.Decisions {
			decisionIDs = append(decisionIDs, decision.ID)
		}
		ruleIDs := make([]string, 0, len(document.Rules))
		for _, rule := range document.Rules {
			ruleIDs = append(ruleIDs, rule.ID)
		}
		eventIDs := make([]string, 0, len(document.Events))
		for _, event := range document.Events {
			eventIDs = append(eventIDs, event.ID)
		}
		recorded, err := client.RecordContext(stdcontext.Background(), config.Organization, api.ContextRecordInput{
			Repository:  config.Repository,
			Agent:       *agent,
			Role:        *role,
			ContextHash: document.ContextHash,
			DecisionIDs: decisionIDs,
			RuleIDs:     ruleIDs,
			EventIDs:    eventIDs,
			SessionID:   *sessionID,
			Summary:     *summary,
			Source:      "agc CLI",
			RecordedAt:  time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(errorOutput, "Recorded context %s with agc server\n", recorded.ID)
	} else if strings.TrimSpace(*serverURL) != "" || strings.TrimSpace(*serverToken) != "" {
		return errors.New("--server and --token are only valid with context --record")
	}
	switch *format {
	case "markdown":
		fmt.Fprint(output, agentcontext.Markdown(document))
	case "json":
		return writeJSON(output, document)
	default:
		return fmt.Errorf("unsupported format %q; use markdown or json", *format)
	}
	return nil
}

func runSync(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing sync subcommand; use push or pull")
	}
	switch args[0] {
	case "pull":
		return runPull(args[1:], workingDirectory, output, errorOutput)
	case "push":
		return runPush(args[1:], workingDirectory, output, errorOutput)
	default:
		return fmt.Errorf("unknown sync subcommand %q; use push or pull", args[0])
	}
}

func runPush(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("push", errorOutput)
	serverURL := flags.String("server", "", "agc server URL override")
	token := flags.String("token", "", "agc server access token override")
	source := flags.String("source", "agc CLI", "repository sync source label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("push does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	config, client, err := resolveRemote(state, *serverURL, *token)
	if err != nil {
		return err
	}
	result, err := syncRepository(state, config, client, *source)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Pushed %s / %s: %d applied, %d unchanged\n", config.Organization, config.Repository, result.Applied, result.Unchanged)
	for _, conflict := range result.Conflicts {
		fmt.Fprintf(output, "Conflict %s (%s): %s\n", conflict.ID, conflict.UID, conflict.Reason)
	}
	if len(result.Conflicts) > 0 {
		return fmt.Errorf("push completed with %d conflict(s)", len(result.Conflicts))
	}
	submitted, err := submitLocalProposals(state, config, client, *source)
	if err != nil {
		return err
	}
	if submitted > 0 {
		fmt.Fprintf(output, "%d temporary decision(s) submitted for repository review.\n", submitted)
	}
	return nil
}

func submitLocalProposals(state store.State, config model.Config, client *serverclient.Client, source string) (int, error) {
	proposals, err := state.ListProposals()
	if err != nil {
		return 0, err
	}
	submitted := 0
	for _, proposal := range proposals {
		if proposal.Status != "local" {
			continue
		}
		remote, err := client.SubmitProposal(stdcontext.Background(), config.Organization, api.SubmitProposalRequest{Repository: config.Repository, Source: strings.TrimSpace(source), Proposal: proposal})
		if err != nil {
			return submitted, fmt.Errorf("submit local decision %s: %w", proposal.ID, err)
		}
		if _, err := state.MarkProposalSubmitted(proposal.UID, remote); err != nil {
			return submitted, fmt.Errorf("local decision %s accepted by server but could not update local state: %w", proposal.ID, err)
		}
		submitted++
	}
	return submitted, nil
}

func syncRepository(state store.State, config model.Config, client *serverclient.Client, source string) (api.SyncResponse, error) {
	decisions, err := state.ListDecisions()
	if err != nil {
		return api.SyncResponse{}, err
	}
	events, err := state.ListEvents()
	if err != nil {
		return api.SyncResponse{}, err
	}
	response, err := client.Sync(stdcontext.Background(), config.Organization, api.SyncRequest{
		Organization: config.Organization, Repository: config.Repository, Source: strings.TrimSpace(source), RepositoryURL: config.RepositoryURL, SentAt: time.Now().UTC(), Decisions: decisions, Events: events,
	})
	if err != nil {
		return api.SyncResponse{}, err
	}
	return response, nil
}

func runPull(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	flags := newFlagSet("pull", errorOutput)
	serverURL := flags.String("server", "", "agc server URL override")
	token := flags.String("token", "", "agc server access token override")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("pull does not accept positional arguments")
	}
	state, err := store.Find(workingDirectory)
	if err != nil {
		return err
	}
	result, snapshot, err := pullState(state, *serverURL, *token)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Pulled %s / %s: %d applied, %d unchanged", snapshot.Organization.Name, snapshot.Repository, result.Applied, result.Unchanged)
	if result.ResolvedProposals > 0 {
		fmt.Fprintf(output, ", %d local decision(s) reviewed", result.ResolvedProposals)
	}
	if result.ResolvedPromotions > 0 {
		fmt.Fprintf(output, ", %d promotion(s) resolved", result.ResolvedPromotions)
	}
	fmt.Fprintln(output)
	if len(result.Conflicts) > 0 {
		for _, conflict := range result.Conflicts {
			fmt.Fprintf(output, "Conflict %s (%s): %s\n", conflict.ID, conflict.UID, conflict.Reason)
		}
		return fmt.Errorf("pull completed with %d conflict(s)", len(result.Conflicts))
	}
	return nil
}

func pullState(state store.State, serverOverride, tokenOverride string) (store.SnapshotApplyResult, api.SnapshotResponse, error) {
	config, client, err := resolveRemote(state, serverOverride, tokenOverride)
	if err != nil {
		return store.SnapshotApplyResult{}, api.SnapshotResponse{}, err
	}
	snapshot, err := client.Snapshot(stdcontext.Background(), config.Organization, config.Repository)
	if err != nil {
		return store.SnapshotApplyResult{}, api.SnapshotResponse{}, err
	}
	result, err := state.ApplyRemoteSnapshot(snapshot.Decisions, snapshot.Rules, snapshot.Events, snapshot.ResolvedProposals, snapshot.ResolvedPromotions)
	if err != nil {
		return result, snapshot, err
	}
	return result, snapshot, nil
}

func resolveRemote(state store.State, serverOverride, tokenOverride string) (model.Config, *serverclient.Client, error) {
	config, err := state.LoadConfig()
	if err != nil {
		return model.Config{}, nil, err
	}
	serverURL := strings.TrimSpace(serverOverride)
	if serverURL == "" {
		serverURL = strings.TrimSpace(config.ServerURL)
	}
	if serverURL == "" {
		return model.Config{}, nil, errors.New("no AGC Server is configured; run agc login first")
	}
	accessToken := strings.TrimSpace(tokenOverride)
	if accessToken == "" {
		credentials, credentialErr := state.LoadCredentials()
		if credentialErr != nil {
			return model.Config{}, nil, credentialErr
		}
		accessToken = credentials.AccessToken
	}
	client, err := serverclient.New(serverURL, accessToken)
	if err != nil {
		return model.Config{}, nil, err
	}
	return config, client, nil
}

func runServer(args []string, workingDirectory string, output, errorOutput io.Writer) error {
	if len(args) == 0 || args[0] != "start" {
		return errors.New("missing server subcommand; use server start")
	}
	flags := newFlagSet("server start", errorOutput)
	listen := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	dataDir := flags.String("data-dir", filepath.Join(workingDirectory, ".agc-server"), "durable server data directory")
	webDir := filepath.Join(workingDirectory, "web", "dist")
	defaultToken := os.Getenv("AGC_SERVER_TOKEN")
	if defaultToken == "" {
		defaultToken = os.Getenv("AC_SERVER_TOKEN")
	}
	token := flags.String("token", defaultToken, "optional bearer token (or AGC_SERVER_TOKEN)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("server start does not accept positional arguments")
	}
	if info, err := os.Stat(filepath.Join(webDir, "index.html")); err != nil || info.IsDir() {
		return fmt.Errorf("React dashboard not found at %s; run npm run build in %s", filepath.Join(webDir, "index.html"), filepath.Join(workingDirectory, "web"))
	}
	serverInstance, err := server.OpenWithOptions(server.Options{
		DataPath: filepath.Join(*dataDir, "server-state.json"),
		Token:    *token,
		WebDir:   webDir,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "agc server listening at http://%s\n", *listen)
	fmt.Fprintf(output, "Dashboard: http://%s/\n", *listen)
	if strings.TrimSpace(*token) == "" {
		fmt.Fprintln(errorOutput, "Warning: no bearer token configured; keep this development server on a trusted network.")
	}
	err = serverInstance.ListenAndServe(*listen)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type promptSession struct {
	reader  *bufio.Reader
	output  io.Writer
	enabled bool
}

func newPromptSession(input io.Reader, output io.Writer) *promptSession {
	enabled := false
	if file, ok := input.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			enabled = info.Mode()&os.ModeCharDevice != 0
		}
	}
	return &promptSession{reader: bufio.NewReader(input), output: output, enabled: enabled}
}

func (p *promptSession) Enabled() bool {
	return p != nil && p.enabled
}

func (p *promptSession) String(label, fallback string) (string, error) {
	if !p.Enabled() {
		return fallback, nil
	}
	if fallback == "" {
		fmt.Fprintf(p.output, "%s: ", label)
	} else {
		fmt.Fprintf(p.output, "%s [%s]: ", label, fallback)
	}
	line, err := p.reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return "", fmt.Errorf("read %s: %w", strings.ToLower(label), err)
	}
	value := strings.TrimSpace(line)
	if value == "" {
		value = fallback
	}
	if value == "" {
		return "", fmt.Errorf("%s is required", strings.ToLower(label))
	}
	return value, nil
}

func (p *promptSession) YesNo(label string, fallback bool) (bool, error) {
	if !p.Enabled() {
		return fallback, nil
	}
	defaultLabel := "y/N"
	if fallback {
		defaultLabel = "Y/n"
	}
	for {
		fmt.Fprintf(p.output, "%s [%s]: ", label, defaultLabel)
		line, err := p.reader.ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
			return false, fmt.Errorf("read %s: %w", strings.ToLower(label), err)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return fallback, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(p.output, "Please answer y or n.")
		}
	}
}

func newFlagSet(name string, errorOutput io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	return flags
}

func flagProvided(flags *flag.FlagSet, name string) bool {
	provided := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == name {
			provided = true
		}
	})
	return provided
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printUsage(output io.Writer) {
	fmt.Fprint(output, `agc — local-first agent consensus management

Usage:
  agc init [--org NAME] [--repo NAME] [--server URL] [--token TOKEN] [--no-remote]
  agc status
  agc login [--server URL] [--token TOKEN]
  agc push [--server URL] [--token TOKEN]
  agc pull [--server URL] [--token TOKEN]
  agc decision create --title TITLE --statement TEXT --scope ROLE[,ROLE] [--owner TEAM]
  agc decision list [--status STATUS] [--format table|json]
  agc decision promote DECISION_ID [--server URL] [--token TOKEN]
  agc event create --title TITLE --statement TEXT [--scope ROLE[,ROLE]]
  agc event list [--format table|json]
  agc promotion list [--format table|json]
  agc context [--role ROLE] [--agent NAME] [--format markdown|json] [--record]
  agc server start [--listen 127.0.0.1:8080] [--data-dir PATH] [--token TOKEN]
  agc version

agc init creates .agc, then optionally configures a server and pulls state.
decision create writes a temporary local decision in .agc/local; agc push
submits it for repository review. Only an approved review creates a repository
decision. Organization rules arrive through agc pull after a separate explicit
agc decision promote workflow and human review.

State is stored in .agc and is safe to commit to Git. credentials.yaml is
kept locally and ignored by .agc/.gitignore.
`)
}
