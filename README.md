# agent-consensus (`agc`)

`agc` is a local-first consensus layer for AI-agent teams. Its ownership model
is deliberately small and explicit:

```text
organization: Rules (stored as Policy entities)
repository:   Decisions + Events (factual cases)
```

A created decision starts as a temporary local rule. It is not visible to
other agents or repositories when pushed. A reviewer must first approve it to
create a repository Decision. Only then may a repository owner explicitly
promote it; a second review creates an organization Rule.

```text
decision create (local) → push → repository review → Decision → promote → Rule review → Rule → pull
```

## Build the dashboard

The React/Vite dashboard lives in `web/` and is served by `agc server start`.

```powershell
cd web
npm install
npm run build
```

Start the local server from the repository root:

```powershell
agc server start
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). For a shared development
server, set a bearer token before starting it:

```powershell
$env:AGC_SERVER_TOKEN = "replace-with-a-long-random-token"
agc server start
```

## Repository workflow

Initialize a repository:

```powershell
agc init --org payment-team --repo payment-service `
  --server http://127.0.0.1:8080 --token $env:AGC_ACCESS_TOKEN
```

Create a temporary local decision and a factual case record. Both immediately
enter the current repository’s local agent context.

```powershell
agc decision create `
  --title "Payment API idempotency" `
  --statement "All payment writes require an idempotency key." `
  --scope backend,frontend `
  --owner architecture-team

agc event create `
  --title "Refund retry incident" `
  --statement "A retry after a timeout caused a duplicate refund." `
  --scope backend

agc push
```

`agc push` synchronizes repository decisions/events and submits new temporary
local decisions for remote review. Pending and rejected proposals are never
included in another agent’s pull. Approval creates a `D-###` decision only in
the source repository; rejected items remain temporary local rules.

When a decision should guide every repository, explicitly request elevation:

```powershell
agc decision promote D-001
```

The promotion appears in the dashboard’s review queue. Approval creates an
organization `R-###` Rule. Pull it into repositories as shared context:

```powershell
agc pull
agc context --role backend --agent codex --record
```

The original `D-001` remains in `.agc/decisions/`; the approved shared rule is
written to `.agc/rules/`. Events never enter the promotion workflow.

## Local files

```text
.agc/
├── config.yaml          # organization, repository, optional server URL
├── credentials.yaml     # local access token; ignored by .agc/.gitignore
├── local/               # temporary decisions awaiting/rejected by review
├── decisions/           # this repository’s decisions
├── events/              # this repository’s factual case records
├── rules/               # pulled organization consensus (Policy entities)
├── promotions/          # local audit trail for explicit promotion requests
├── knowledge/
├── sessions/
└── context/
```

All state except `credentials.yaml` is ordinary YAML and suitable for Git
review.

## API surface

The dashboard and CLI share `/api/v1`:

- `POST /organizations/{organization}/sync` — sync one repository’s decisions and events
- `POST /organizations/{organization}/proposals` — submit a temporary decision for repository review
- `PATCH /organizations/{organization}/proposals/{proposal}` — approve or reject it as a repository decision
- `POST /organizations/{organization}/events` — submit one repository event
- `POST /organizations/{organization}/promotions` — explicitly request decision elevation
- `PATCH /organizations/{organization}/promotions/{promotion}` — approve or reject an organization Rule
- `GET /organizations/{organization}/snapshot?repository=...` — retrieve org rules and one repository’s state
- `GET /organizations/{organization}/context?repository=...` — compile role-specific context
- `POST /organizations/{organization}/context-records` — audit a delivered context

The server persists JSON at `.agc-server/server-state.json` by default. It is
designed for local and small-team use; protect shared deployments with TLS and
an authenticated gateway.
