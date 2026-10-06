# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases,
records measurements, handles failed tolerance decisions, and seals traceable
evidence records.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow record-measurement-batch
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, not a unit test.

The unit and integration test suite (including the batch atomicity, idempotent
replay, and concurrent-submit tests) runs with:

```bash
go test ./... -race
```

## API

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/measurements/batch`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

### Batch measurements

`POST /cases/{id}/measurements/batch` commits a case-scoped batch of readings
atomically. `batch_id` is the idempotency key; `client_ref` on each reading is
an optional token echoed in the matching result:

```json
{
  "batch_id": "station-3-20261006-01",
  "readings": [
    {"observed": 25.10, "technician": "Rui", "client_ref": "point-1"},
    {"observed": 24.85, "technician": "Rui", "client_ref": "point-2"}
  ]
}
```

The response carries one result per reading, located by `index` and
`client_ref`. All readings in tolerance move the case to `review_ready`;
any out-of-tolerance reading moves it to `recheck_required`. If any reading is
illegal or the case is not `awaiting_measurement`, the whole request fails
without leaving measurements, case state, or audit events behind. Retrying the
same `batch_id` returns the stored response (`"replayed": true`, HTTP 200) and
never duplicates measurements; the same key with different readings returns
`batch_already_submitted`.

## Layout

- `cmd/metrology-service`: executable HTTP server.
- `cmd/workflowcheck`: bounded workflow verification commands.
- `internal/httpapi`: route and JSON boundary.
- `internal/assurance`: workflow orchestration.
- `internal/domain`: state rules and invariants.
- `internal/repository`: copy-on-write in-memory persistence.
- `internal/contracts`: API request and response models.
- `internal/bootstrap`: concrete dependency wiring.

## Inputs And Outputs

Inputs are JSON HTTP requests. Outputs are JSON response envelopes with either a
resource view or a stable error object:

```json
{
  "error": {
    "code": "state_conflict",
    "message": "case is not awaiting measurement"
  }
}
```

The service has no environment variables and no external dependencies.
