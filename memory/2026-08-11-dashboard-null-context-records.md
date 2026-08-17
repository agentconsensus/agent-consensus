# Dashboard null context-records investigation — 2026-08-11

## Symptom

The management dashboard crashed on load with `Cannot read properties of null (reading 'length')`.

## Root cause

`detailFromOrganization` copied context records with `append([]api.ContextRecord(nil), records...)`. For an organization with no records, this produced a nil slice. Go encoded that as JSON `null`, while the React dashboard correctly treats `context_records` as an array and reads `.length`.

## Reproduction evidence

Before the fix, `GET /api/v1/organizations/a` returned `"context_records":null` for an organization with no context records.

## Fix

The server now initializes `ContextRecords` with a non-nil empty slice before copying values, so its JSON representation is always an array (`[]`).

## Regression coverage

`TestOrganizationDetailEmitsEmptyContextRecordsArray` verifies the raw HTTP JSON response contains `"context_records":[]` for a newly created organization.

## Verification

- `go test ./...` passed.
- `go vet ./...` passed.
- `npm --prefix web run build` passed.
- The restarted local server returns `"context_records":[]` and `/healthz` reports `ok`.

## Status

DONE
