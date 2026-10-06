package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"example.com/solo-0021-metrology-assurance-suite/internal/assurance"
	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type batchHarness struct {
	t      *testing.T
	server *httptest.Server
}

func newBatchHarness(t *testing.T) *batchHarness {
	t.Helper()
	service := assurance.NewService(repository.New())
	server := httptest.NewServer(NewHandler(service))
	t.Cleanup(server.Close)
	return &batchHarness{t: t, server: server}
}

func (h *batchHarness) do(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(recorder, request)
	return recorder
}

// doURL performs a real network round-trip, used for the concurrency check.
func (h *batchHarness) doURL(method, path string, body any) (*http.Response, []byte) {
	h.t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		h.t.Fatalf("marshal: %v", err)
	}
	response, err := h.server.Client().Post(h.server.URL+path, "application/json", bytes.NewReader(encoded))
	if err != nil {
		h.t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(response.Body); err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return response, buf.Bytes()
}

func (h *batchHarness) createOpenCase(tag string) string {
	h.t.Helper()
	deviceRecorder := h.do(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag, "model": "m", "room": "r", "steward": "s",
	})
	if deviceRecorder.Code != http.StatusCreated {
		h.t.Fatalf("register device status = %d body = %s", deviceRecorder.Code, deviceRecorder.Body.String())
	}
	var device struct {
		Device struct {
			ID string `json:"id"`
		} `json:"device"`
	}
	decodeJSONBody(h.t, deviceRecorder.Body.Bytes(), &device)

	caseRecorder := h.do(http.MethodPost, "/cases", map[string]any{
		"device_id": device.Device.ID, "nominal": 25.0, "tolerance": 0.5, "steward": "s",
	})
	if caseRecorder.Code != http.StatusCreated {
		h.t.Fatalf("open case status = %d body = %s", caseRecorder.Code, caseRecorder.Body.String())
	}
	var opened struct {
		Case struct {
			ID string `json:"id"`
		} `json:"case"`
	}
	decodeJSONBody(h.t, caseRecorder.Body.Bytes(), &opened)
	return opened.Case.ID
}

func decodeJSONBody(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", string(raw), err)
	}
}

func batchBody(observed float64, technician string) map[string]any {
	return map[string]any{"observed": observed, "technician": technician}
}

func TestBatchEndpointSuccessAndReplay(t *testing.T) {
	h := newBatchHarness(t)
	caseID := h.createOpenCase("MET-H1")

	created := h.do(http.MethodPost, "/cases/"+caseID+"/measurements/batch", map[string]any{
		"batch_id": "batch-A",
		"readings": []map[string]any{
			batchBody(25.1, "Rui"),
			batchBody(24.8, "Lin"),
		},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", created.Code, created.Body.String())
	}
	var first contracts.BatchMeasurementsResponse
	decodeJSONBody(t, created.Body.Bytes(), &first)
	if first.Replayed || len(first.Results) != 2 || first.Case.State != "review_ready" {
		t.Fatalf("unexpected first batch: %+v", first)
	}

	replay := h.do(http.MethodPost, "/cases/"+caseID+"/measurements/batch", map[string]any{
		"batch_id": "batch-A",
		"readings": []map[string]any{
			batchBody(25.1, "Rui"),
			batchBody(24.8, "Lin"),
		},
	})
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status = %d body = %s", replay.Code, replay.Body.String())
	}
	var repeated contracts.BatchMeasurementsResponse
	decodeJSONBody(t, replay.Body.Bytes(), &repeated)
	if !repeated.Replayed {
		t.Fatal("replay response did not set replayed=true")
	}
	if repeated.Results[0].Measurement.ID != first.Results[0].Measurement.ID {
		t.Fatal("replay minted a new measurement id")
	}
}

func TestBatchEndpointRollbackOnInvalidReading(t *testing.T) {
	h := newBatchHarness(t)
	caseID := h.createOpenCase("MET-H2")

	failed := h.do(http.MethodPost, "/cases/"+caseID+"/measurements/batch", map[string]any{
		"batch_id": "bad",
		"readings": []map[string]any{
			batchBody(25.1, "Rui"),
			batchBody(25.2, "  "),
			batchBody(25.3, "Rui"),
		},
	})
	if failed.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", failed.Code, failed.Body.String())
	}
	var errBody contracts.ErrorResponse
	decodeJSONBody(t, failed.Body.Bytes(), &errBody)
	if errBody.Error.Code != "invalid_input" {
		t.Fatalf("error code = %q", errBody.Error.Code)
	}
	if !strings.Contains(errBody.Error.Message, "index 1") {
		t.Fatalf("message does not locate the offending reading: %q", errBody.Error.Message)
	}

	read := h.do(http.MethodGet, "/cases/"+caseID, nil)
	var view contracts.CaseResponse
	decodeJSONBody(t, read.Body.Bytes(), &view)
	if view.Case.State != "awaiting_measurement" || len(view.Measurements) != 0 {
		t.Fatalf("failed batch left state=%q measurements=%d", view.Case.State, len(view.Measurements))
	}
	if len(view.Events) != 1 {
		t.Fatalf("failed batch left %d events", len(view.Events))
	}
}

func TestBatchEndpointDivergentKeyConflict(t *testing.T) {
	h := newBatchHarness(t)
	caseID := h.createOpenCase("MET-H3")

	first := h.do(http.MethodPost, "/cases/"+caseID+"/measurements/batch", map[string]any{
		"batch_id": "batch-A",
		"readings": []map[string]any{batchBody(25.1, "Rui")},
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d body = %s", first.Code, first.Body.String())
	}

	diverged := h.do(http.MethodPost, "/cases/"+caseID+"/measurements/batch", map[string]any{
		"batch_id": "batch-A",
		"readings": []map[string]any{batchBody(25.4, "Lin")},
	})
	if diverged.Code != http.StatusConflict {
		t.Fatalf("diverged status = %d body = %s", diverged.Code, diverged.Body.String())
	}
	var errBody contracts.ErrorResponse
	decodeJSONBody(t, diverged.Body.Bytes(), &errBody)
	if errBody.Error.Code != "batch_already_submitted" {
		t.Fatalf("error code = %q, want batch_already_submitted", errBody.Error.Code)
	}
}

func TestBatchEndpointUnknownCase(t *testing.T) {
	h := newBatchHarness(t)
	response := h.do(http.MethodPost, "/cases/case-nope/measurements/batch", map[string]any{
		"batch_id": "b",
		"readings": []map[string]any{batchBody(1, "Rui")},
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestBatchEndpointConcurrentIdenticalRequests(t *testing.T) {
	h := newBatchHarness(t)
	caseID := h.createOpenCase("MET-H4")
	path := "/cases/" + caseID + "/measurements/batch"
	payload := map[string]any{
		"batch_id": "concurrent",
		"readings": []map[string]any{
			batchBody(25.1, "Rui"),
			batchBody(24.7, "Rui"),
		},
	}

	const clients = 8
	var wg sync.WaitGroup
	statuses := make([]int, clients)
	ids := make([]string, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			response, raw := h.doURL(http.MethodPost, path, payload)
			defer response.Body.Close()
			statuses[i] = response.StatusCode
			var view contracts.BatchMeasurementsResponse
			if response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusOK {
				decodeJSONBody(t, raw, &view)
				ids[i] = view.Results[0].Measurement.ID
			}
		}(i)
	}
	wg.Wait()

	created, ok := 0, 0
	firstID := ""
	for i, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
			firstID = ids[i]
		case http.StatusOK:
			ok++
		default:
			t.Fatalf("unexpected concurrent status %d", status)
		}
	}
	if created != 1 || ok != clients-1 {
		t.Fatalf("created=%d replayed=%d, want 1 and %d", created, ok, clients-1)
	}
	for _, id := range ids {
		if id != "" && id != firstID {
			t.Fatalf("concurrent requests created different measurement ids: %q vs %q", id, firstID)
		}
	}
}
