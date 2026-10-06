package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":          checkRegisterDevice,
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"record-measurement-batch": checkRecordMeasurementBatch,
		"reopen-verification-case": checkReopenVerificationCase,
		"seal-verification":        checkSealVerification,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkRecordMeasurementBatch() error {
	h := newHarness()
	defer h.close()

	// 1. Happy path: three in-tolerance readings land as one batch and each
	// result is locatable by request index.
	caseID, err := h.openAwaitingCase("MET-1501")
	if err != nil {
		return err
	}
	readings := []map[string]any{
		batchReading(25.1, "Rui"),
		batchReading(25.2, "Rui"),
		batchReading(24.8, "Lin"),
	}
	first, err := h.recordMeasurementBatch(caseID, "batch-1", readings)
	if err != nil {
		return err
	}
	if err := expectStatus(first, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(first.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("all-accepted batch state = %q, want review_ready", state)
	}
	replayed, err := nestedBool(first.Body, "replayed")
	if err != nil {
		return err
	}
	if replayed {
		return errors.New("original submission was marked as replayed")
	}
	results, err := nestedSlice(first.Body, "results")
	if err != nil {
		return err
	}
	if len(results) != 3 {
		return fmt.Errorf("results length = %d, want 3", len(results))
	}
	for index, value := range []float64{25.1, 25.2, 24.8} {
		resultIndex, err := nestedInt(first.Body, "results", index, "index")
		if err != nil {
			return err
		}
		if resultIndex != index {
			return fmt.Errorf("result %d reported index %d", index, resultIndex)
		}
		observed, err := nestedFloat(first.Body, "results", index, "measurement", "observed")
		if err != nil {
			return err
		}
		if observed != value {
			return fmt.Errorf("result %d observed = %v, want %v", index, observed, value)
		}
		eventID, err := nestedStringAny(first.Body, "results", index, "event_id")
		if err != nil {
			return err
		}
		if eventID == "" {
			return fmt.Errorf("result %d has no event id", index)
		}
	}
	measurements, err := nestedSlice(first.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 3 {
		return fmt.Errorf("measurements length = %d, want 3", len(measurements))
	}
	events, err := nestedSlice(first.Body, "events")
	if err != nil {
		return err
	}
	// One case_opened event plus one measurement event per reading.
	if len(events) != 4 {
		return fmt.Errorf("audit events length = %d, want 4 (one per reading plus case_opened)", len(events))
	}
	if count := countEventsByKind(first.Body, "measurement_accepted"); count != 3 {
		return fmt.Errorf("measurement_accepted events = %d, want 3", count)
	}
	version, err := nestedInt(first.Body, "case", "version")
	if err != nil {
		return err
	}
	if version != 2 {
		return fmt.Errorf("case version = %d, want 2 after one batch decision", version)
	}

	// 2. Retry with the same key and payload replays the original facts (200,
	// replayed=true, same ids) even though the case is now review_ready.
	retry, err := h.recordMeasurementBatch(caseID, "batch-1", readings)
	if err != nil {
		return err
	}
	if err := expectStatus(retry, http.StatusOK); err != nil {
		return err
	}
	flag, err := nestedBool(retry.Body, "replayed")
	if err != nil {
		return err
	}
	if !flag {
		return errors.New("retried batch was not marked as replayed")
	}
	retryResults, err := nestedSlice(retry.Body, "results")
	if err != nil {
		return err
	}
	if len(retryResults) != 3 {
		return fmt.Errorf("replayed results length = %d, want 3", len(retryResults))
	}
	for index := range readings {
		replayedID, err := nestedStringAny(retry.Body, "results", index, "measurement", "id")
		if err != nil {
			return err
		}
		originalID, err := nestedStringAny(first.Body, "results", index, "measurement", "id")
		if err != nil {
			return err
		}
		if replayedID != originalID {
			return fmt.Errorf("replay at %d created new measurement %s, want %s", index, replayedID, originalID)
		}
	}
	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	persisted, err := nestedSlice(read.Body, "measurements")
	if err != nil {
		return err
	}
	if len(persisted) != 3 {
		return fmt.Errorf("retry duplicated measurements: %d entries", len(persisted))
	}
	persistedEvents, err := nestedSlice(read.Body, "events")
	if err != nil {
		return err
	}
	if len(persistedEvents) != 4 {
		return fmt.Errorf("retry duplicated audit events: %d entries", len(persistedEvents))
	}

	// 3. Same key with a different payload is rejected as a conflict and
	// changes nothing.
	tampered, err := h.recordMeasurementBatch(caseID, "batch-1", []map[string]any{batchReading(25.3, "Rui")})
	if err != nil {
		return err
	}
	if err := expectStatus(tampered, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(tampered, "idempotency_key_reuse"); err != nil {
		return err
	}

	// 4. A new batch against the now review_ready case fails state validation
	// without writing anything.
	stateBatch, err := h.recordMeasurementBatch(caseID, "batch-2", []map[string]any{batchReading(25.1, "Rui")})
	if err != nil {
		return err
	}
	if err := expectStatus(stateBatch, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(stateBatch, "state_conflict"); err != nil {
		return err
	}

	// 5. An invalid row (blank technician) rejects the whole batch and points
	// at the offending index; no half-batch survives.
	mixedCaseID, err := h.openAwaitingCase("MET-1502")
	if err != nil {
		return err
	}
	invalid, err := h.recordMeasurementBatch(mixedCaseID, "batch-bad", []map[string]any{
		batchReading(25.1, "Rui"),
		batchReading(25.0, "   "),
	})
	if err != nil {
		return err
	}
	if err := expectStatus(invalid, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(invalid, "invalid_input"); err != nil {
		return err
	}
	details, err := nestedSlice(invalid.Body, "error", "details")
	if err != nil {
		return err
	}
	if len(details) != 1 {
		return fmt.Errorf("invalid batch reported %d details, want 1", len(details))
	}
	detailIndex, err := nestedInt(invalid.Body, "error", "details", 0, "index")
	if err != nil {
		return err
	}
	if detailIndex != 1 {
		return fmt.Errorf("invalid detail index = %d, want 1", detailIndex)
	}
	detailCode, err := nestedStringAny(invalid.Body, "error", "details", 0, "code")
	if err != nil {
		return err
	}
	if detailCode != "invalid_input" {
		return fmt.Errorf("invalid detail code = %q", detailCode)
	}
	mixedRead, err := h.request(http.MethodGet, "/cases/"+mixedCaseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(mixedRead, http.StatusOK); err != nil {
		return err
	}
	mixedState, err := nestedString(mixedRead.Body, "case", "state")
	if err != nil {
		return err
	}
	if mixedState != "awaiting_measurement" {
		return fmt.Errorf("failed batch moved case to %q", mixedState)
	}
	mixedMeasurements, err := nestedSlice(mixedRead.Body, "measurements")
	if err != nil {
		return err
	}
	if len(mixedMeasurements) != 0 {
		return fmt.Errorf("failed batch left %d measurements behind", len(mixedMeasurements))
	}
	mixedEvents, err := nestedSlice(mixedRead.Body, "events")
	if err != nil {
		return err
	}
	// The failed batch leaves only the original case_opened event; no
	// measurement event may survive the rollback.
	if len(mixedEvents) != 1 {
		return fmt.Errorf("failed batch left %d audit events behind, want 1", len(mixedEvents))
	}
	if count := countEventsByKind(mixedRead.Body, "measurement_accepted"); count != 0 {
		return fmt.Errorf("failed batch left %d accepted events", count)
	}
	if count := countEventsByKind(mixedRead.Body, "measurement_rejected"); count != 0 {
		return fmt.Errorf("failed batch left %d rejected events", count)
	}

	// 6. After the failed batch, a corrected batch succeeds and one
	// out-of-tolerance reading drives the case to recheck_required.
	corrected, err := h.recordMeasurementBatch(mixedCaseID, "batch-good", []map[string]any{
		batchReading(25.1, "Rui"),
		batchReading(26.0, "Rui"),
	})
	if err != nil {
		return err
	}
	if err := expectStatus(corrected, http.StatusCreated); err != nil {
		return err
	}
	correctedState, err := nestedString(corrected.Body, "case", "state")
	if err != nil {
		return err
	}
	if correctedState != "recheck_required" {
		return fmt.Errorf("mixed-tolerance batch state = %q, want recheck_required", correctedState)
	}
	if count := countEventsByKind(corrected.Body, "measurement_accepted"); count != 1 {
		return fmt.Errorf("corrected batch accepted events = %d, want 1", count)
	}
	if count := countEventsByKind(corrected.Body, "measurement_rejected"); count != 1 {
		return fmt.Errorf("corrected batch rejected events = %d, want 1", count)
	}

	// 7. Missing idempotency key is rejected before any work happens.
	missingKey, err := h.request(http.MethodPost, "/cases/"+mixedCaseID+"/measurements/batch", map[string]any{
		"readings": []map[string]any{batchReading(25.1, "Rui")},
	})
	if err != nil {
		return err
	}
	if err := expectStatus(missingKey, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(missingKey, "invalid_input"); err != nil {
		return err
	}

	// 8. Batch against a missing case is a 404.
	missingCase, err := h.recordMeasurementBatch("case-9999", "batch-x", []map[string]any{batchReading(25.1, "Rui")})
	if err != nil {
		return err
	}
	if err := expectStatus(missingCase, http.StatusNotFound); err != nil {
		return err
	}

	// 9. Concurrent retries of one batch must commit exactly once.
	concurrentCaseID, err := h.openAwaitingCase("MET-1503")
	if err != nil {
		return err
	}
	concurrentReadings := []map[string]any{batchReading(25.1, "Rui"), batchReading(25.2, "Lin")}
	type outcome struct {
		response apiResponse
		err      error
	}
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := h.recordMeasurementBatch(concurrentCaseID, "concurrent-batch", concurrentReadings)
			outcomes <- outcome{response: response, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)
	created := 0
	replayedCount := 0
	for item := range outcomes {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			replayedCount++
		default:
			return fmt.Errorf("unexpected concurrent batch status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if created != 1 || replayedCount != 1 {
		return fmt.Errorf("concurrent batch outcomes: %d created, %d replayed", created, replayedCount)
	}
	concurrentRead, err := h.request(http.MethodGet, "/cases/"+concurrentCaseID, nil)
	if err != nil {
		return err
	}
	concurrentMeasurements, err := nestedSlice(concurrentRead.Body, "measurements")
	if err != nil {
		return err
	}
	if len(concurrentMeasurements) != 2 {
		return fmt.Errorf("concurrent batch left %d measurements, want 2", len(concurrentMeasurements))
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func batchReading(observed float64, technician string) map[string]any {
	return map[string]any{
		"observed":   observed,
		"technician": technician,
	}
}

func (h *harness) recordMeasurementBatch(caseID, idempotencyKey string, readings []map[string]any) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements/batch", map[string]any{
		"idempotency_key": idempotencyKey,
		"readings":        readings,
	})
}

func (h *harness) openAwaitingCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	return nestedString(opened.Body, "case", "id")
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedInt(root map[string]any, path ...any) (int, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%v is not a number", path)
	}
	return int(number), nil
}

func nestedFloat(root map[string]any, path ...any) (float64, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%v is not a number", path)
	}
	return number, nil
}

func nestedSlice(root map[string]any, path ...any) ([]any, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return nil, err
	}
	// A nil slice serializes to JSON null, which means an empty collection.
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%v is not an array", path)
	}
	return items, nil
}

func nestedStringAny(root map[string]any, path ...any) (string, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%v is not a string", path)
	}
	return text, nil
}

func hasEvent(root map[string]any, kind string) bool {
	return countEventsByKind(root, kind) > 0
}

func countEventsByKind(root map[string]any, kind string) int {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return 0
	}
	count := 0
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			count++
		}
	}
	return count
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
