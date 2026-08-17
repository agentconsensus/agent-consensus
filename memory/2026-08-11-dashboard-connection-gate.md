# Dashboard connection gate investigation — 2026-08-11

## Symptom

The dashboard displayed the "First organization" creation screen even when the AGC API was not connected or required an access token.

## Root cause

`App.jsx` used `detail === null` as the only render condition for the creation screen. Both a successful empty organization list and a failed or unauthorized API request leave `detail` null, so two distinct states were rendered identically.

## Reproduction evidence

The running server reported `auth_required: true` from `/healthz`, while an unauthenticated `GET /api/v1/organizations` returned HTTP 401. The former implementation would still render the creation screen because no detail had been loaded.

## Fix

The dashboard now tracks an explicit connection phase. It renders the consensus console only after the organizations API succeeds and the phase becomes `connected`. Checking, unavailable, and token-required states render a connection gate instead. A successfully connected empty server now also reports `Connected` before showing the create-organization screen.

## Regression coverage

`web/src/connection.test.mjs` verifies that only the `connected` phase unlocks the console.

## Verification

- `node --test web/src/connection.test.mjs` passed.
- `npm --prefix web run build` passed.
- `go test ./...` and `go vet ./...` passed.
- The active server serves the rebuilt dashboard and returns HTTP 401 for an unauthenticated protected API request.

## Related

The access-token input is shown only when the health endpoint reports that authentication is required; static page access remains public while API access is protected.

## Status

DONE
