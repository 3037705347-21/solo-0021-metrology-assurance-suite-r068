package assurance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type fixture struct {
	service *Service
	repo    *repository.Repository
	clock   *fixedClock
}

type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := repository.New()
	service := NewService(repo)
	clock := &fixedClock{now: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)}
	service.now = clock.get
	return &fixture{service: service, repo: repo, clock: clock}
}

func (f *fixture) openCase(t *testing.T, tag string) string {
	t.Helper()
	ctx := context.Background()
	device, err := f.service.RegisterDevice(ctx, contracts.RegisterDeviceRequest{
		AssetTag: tag, Model: "Bench Meter", Room: "Room A", Steward: "Lin",
	})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	view, err := f.service.OpenCase(ctx, contracts.OpenCaseRequest{
		DeviceID: device.ID, Nominal: 25.0, Tolerance: 0.5, Steward: "Qiao",
	})
	if err != nil {
		t.Fatalf("OpenCase: %v", err)
	}
	return view.Case.ID
}

func reading(observed float64, technician, ref string) contracts.MeasurementReadingInput {
	return contracts.MeasurementReadingInput{Observed: observed, Technician: technician, ClientRef: ref}
}

func TestBatchCommitsEveryReadingAndOneTransition(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B1")
	ctx := context.Background()

	view, err := f.service.RecordMeasurementBatch(ctx, caseID, contracts.RecordMeasurementBatchRequest{
		BatchID: "batch-A",
		Readings: []contracts.MeasurementReadingInput{
			reading(25.1, "Rui", "p1"),
			reading(24.7, "Rui", ""),
			reading(25.0, "Lin", "p3"),
		},
	})
	if err != nil {
		t.Fatalf("RecordMeasurementBatch: %v", err)
	}
	if view.Replayed {
		t.Fatal("new batch was marked replayed")
	}
	if view.Case.State != domain.CaseReviewReady {
		t.Fatalf("state = %q, want review_ready", view.Case.State)
	}
	if view.Case.Version != 2 {
		t.Fatalf("version = %d, want 2", view.Case.Version)
	}
	if len(view.Results) != 3 || len(view.Measurements) != 3 {
		t.Fatalf("results=%d measurements=%d, want 3 each", len(view.Results), len(view.Measurements))
	}
	for index, result := range view.Results {
		if result.Index != index {
			t.Fatalf("result %d has index %d", index, result.Index)
		}
		if result.Measurement.CaseID != caseID {
			t.Fatalf("result %d measurement case = %q", index, result.Measurement.CaseID)
		}
		if !result.Measurement.Accepted {
			t.Fatalf("in-tolerance result %d reported rejected", index)
		}
	}
	if view.Results[0].ClientRef != "p1" || view.Results[2].ClientRef != "p3" {
		t.Fatalf("client refs were not echoed: %q %q", view.Results[0].ClientRef, view.Results[2].ClientRef)
	}
	if view.Results[1].ClientRef != "" {
		t.Fatalf("missing ref should stay empty, got %q", view.Results[1].ClientRef)
	}

	// One batch audit fact plus one per reading plus case_opened.
	if len(view.Events) != 5 {
		t.Fatalf("events = %d, want 5 (open + 3 readings + batch)", len(view.Events))
	}
	kinds := map[string]int{}
	for _, event := range view.Events {
		kinds[event.Kind]++
	}
	if kinds["measurement_accepted"] != 3 || kinds["measurement_batch_recorded"] != 1 {
		t.Fatalf("unexpected event kinds: %v", kinds)
	}
}

func TestBatchMixedOutcomeMovesToRecheck(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B2")

	view, err := f.service.RecordMeasurementBatch(context.Background(), caseID, contracts.RecordMeasurementBatchRequest{
		BatchID: "batch-mixed",
		Readings: []contracts.MeasurementReadingInput{
			reading(25.1, "Rui", "ok"),
			reading(26.4, "Rui", "out"),
		},
	})
	if err != nil {
		t.Fatalf("RecordMeasurementBatch: %v", err)
	}
	if view.Case.State != domain.CaseRecheckRequired {
		t.Fatalf("state = %q, want recheck_required", view.Case.State)
	}
	if view.Results[0].Measurement.Accepted != true || view.Results[1].Measurement.Accepted != false {
		t.Fatal("per-reading acceptance does not match tolerance")
	}
	deviation := view.Results[1].Measurement.Deviation
	if math.Abs(deviation-1.4) > 1e-12 {
		t.Fatalf("deviation = %v, want 1.4", deviation)
	}
}

func TestBatchInvalidReadingRollsBackEverything(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B3")
	ctx := context.Background()

	_, err := f.service.RecordMeasurementBatch(ctx, caseID, contracts.RecordMeasurementBatchRequest{
		BatchID: "batch-bad",
		Readings: []contracts.MeasurementReadingInput{
			reading(25.1, "Rui", "a"),
			{Observed: 25.2, Technician: "   ", ClientRef: "b"},
			reading(math.NaN(), "Rui", "c"),
		},
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}

	caseView, err := f.service.Case(ctx, caseID)
	if err != nil {
		t.Fatalf("Case: %v", err)
	}
	if caseView.Case.State != domain.CaseAwaitingMeasurement || caseView.Case.Version != 1 {
		t.Fatalf("case after failed batch: state=%q version=%d", caseView.Case.State, caseView.Case.Version)
	}
	if len(caseView.Measurements) != 0 {
		t.Fatalf("failed batch left %d measurements", len(caseView.Measurements))
	}
	if len(caseView.Events) != 1 || caseView.Events[0].Kind != "case_opened" {
		t.Fatalf("failed batch left audit events: %+v", caseView.Events)
	}

	// The unused idempotency key is not stored: the corrected retry commits.
	retry, err := f.service.RecordMeasurementBatch(ctx, caseID, contracts.RecordMeasurementBatchRequest{
		BatchID: "batch-bad",
		Readings: []contracts.MeasurementReadingInput{
			reading(25.1, "Rui", "a"),
			reading(25.2, "Rui", "b"),
		},
	})
	if err != nil {
		t.Fatalf("corrected retry: %v", err)
	}
	if retry.Replayed || len(retry.Measurements) != 2 {
		t.Fatalf("retry should commit fresh: replayed=%v measurements=%d", retry.Replayed, len(retry.Measurements))
	}
}

func TestBatchValidationFailures(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B4")
	ctx := context.Background()

	cases := []struct {
		name string
		req  contracts.RecordMeasurementBatchRequest
	}{
		{"missing batch id", contracts.RecordMeasurementBatchRequest{BatchID: "  ", Readings: []contracts.MeasurementReadingInput{reading(1, "Rui", "")}}},
		{"no readings", contracts.RecordMeasurementBatchRequest{BatchID: "b", Readings: nil}},
		{"duplicate refs", contracts.RecordMeasurementBatchRequest{BatchID: "b", Readings: []contracts.MeasurementReadingInput{
			reading(1, "Rui", "same"), reading(2, "Rui", "same"),
		}}},
		{"missing technician", contracts.RecordMeasurementBatchRequest{BatchID: "b", Readings: []contracts.MeasurementReadingInput{
			reading(1, "Rui", ""), {Observed: 2, Technician: ""},
		}}},
		{"non-finite observed", contracts.RecordMeasurementBatchRequest{BatchID: "b", Readings: []contracts.MeasurementReadingInput{
			reading(math.Inf(1), "Rui", ""),
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.service.RecordMeasurementBatch(ctx, caseID, tc.req); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}

	t.Run("oversized batch", func(t *testing.T) {
		readings := make([]contracts.MeasurementReadingInput, domain.MaxBatchReadings+1)
		for i := range readings {
			readings[i] = reading(1, "Rui", "")
		}
		_, err := f.service.RecordMeasurementBatch(ctx, caseID, contracts.RecordMeasurementBatchRequest{BatchID: "b", Readings: readings})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput", err)
		}
	})

	// None of the validation failures stored anything.
	view, err := f.service.Case(ctx, caseID)
	if err != nil {
		t.Fatalf("Case: %v", err)
	}
	if len(view.Measurements) != 0 || view.Case.Version != 1 {
		t.Fatalf("validation attempts changed the case: %+v", view.Case)
	}
}

func TestBatchReplayIsIdempotentAfterCaseMovesOn(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B5")
	ctx := context.Background()
	req := contracts.RecordMeasurementBatchRequest{
		BatchID: "batch-A",
		Readings: []contracts.MeasurementReadingInput{
			reading(25.1, "Rui", "p1"),
			reading(24.8, "Rui", "p2"),
		},
	}

	first, err := f.service.RecordMeasurementBatch(ctx, caseID, req)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// The case is now review_ready, which would reject any new measurement,
	// but replaying the stored batch returns the original results unchanged.
	replay, err := f.service.RecordMeasurementBatch(ctx, caseID, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatal("replay was not flagged as replayed")
	}
	for i := range first.Results {
		if replay.Results[i].Measurement.ID != first.Results[i].Measurement.ID {
			t.Fatalf("replay result %d id = %s, want %s", i, replay.Results[i].Measurement.ID, first.Results[i].Measurement.ID)
		}
	}
	if replay.Case.State != domain.CaseReviewReady || replay.Case.Version != 2 {
		t.Fatalf("replay view case = %q v%d", replay.Case.State, replay.Case.Version)
	}
	if len(replay.Measurements) != 2 {
		t.Fatalf("replay sees %d measurements, want the original 2", len(replay.Measurements))
	}

	// A different payload under the same key is a conflict and stores nothing.
	diverged := req
	diverged.Readings = []contracts.MeasurementReadingInput{reading(25.3, "Rui", "p1")}
	if _, err := f.service.RecordMeasurementBatch(ctx, caseID, diverged); !errors.Is(err, domain.ErrBatchReplayed) {
		t.Fatalf("diverged payload error = %v, want ErrBatchReplayed", err)
	}
	again, err := f.service.RecordMeasurementBatch(ctx, caseID, req)
	if err != nil || !again.Replayed {
		t.Fatalf("original batch no longer replays after diverged attempt: %v replayed=%v", err, again.Replayed)
	}
}

func TestBatchReplaySurvivesLaterCaseHistory(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B6")
	ctx := context.Background()
	req := contracts.RecordMeasurementBatchRequest{
		BatchID: "batch-mixed",
		Readings: []contracts.MeasurementReadingInput{
			reading(25.1, "Rui", "ok"),
			reading(26.4, "Rui", "out"),
		},
	}
	first, err := f.service.RecordMeasurementBatch(ctx, caseID, req)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if first.Case.State != domain.CaseRecheckRequired {
		t.Fatalf("state = %q", first.Case.State)
	}

	if _, err := f.service.ReopenCase(ctx, caseID, contracts.ReopenCaseRequest{Actor: "Lin", Reason: "setup replaced"}); err != nil {
		t.Fatalf("ReopenCase: %v", err)
	}

	// Replay returns the stored view even though the live case has since been
	// reopened and received further measurements.
	if _, err := f.service.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{Observed: 25.0, Technician: "Rui"}); err != nil {
		t.Fatalf("RecordMeasurement: %v", err)
	}
	replay, err := f.service.RecordMeasurementBatch(ctx, caseID, req)
	if err != nil {
		t.Fatalf("replay after history: %v", err)
	}
	if !replay.Replayed {
		t.Fatal("expected stored replay after later case history")
	}
	if replay.Case.State != domain.CaseRecheckRequired {
		t.Fatalf("stored replay state = %q, want recheck_required snapshot", replay.Case.State)
	}
	if len(replay.Measurements) != 2 {
		t.Fatalf("stored replay includes %d measurements, want the original 2", len(replay.Measurements))
	}
}

func TestBatchKeyIsScopedPerCase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.openCase(t, "MET-B7A")
	other := f.openCase(t, "MET-B7B")

	req := contracts.RecordMeasurementBatchRequest{
		BatchID:  "shared-key",
		Readings: []contracts.MeasurementReadingInput{reading(25.1, "Rui", "")},
	}
	if _, err := f.service.RecordMeasurementBatch(ctx, first, req); err != nil {
		t.Fatalf("first case batch: %v", err)
	}
	view, err := f.service.RecordMeasurementBatch(ctx, other, req)
	if err != nil {
		t.Fatalf("same key on a different case should be independent: %v", err)
	}
	if view.Replayed {
		t.Fatal("batch id reused on another case was treated as a replay")
	}
}

func TestBatchRejectsWhenCaseNotAwaiting(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B8")
	ctx := context.Background()

	if _, err := f.service.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{Observed: 25.1, Technician: "Rui"}); err != nil {
		t.Fatalf("RecordMeasurement: %v", err)
	}
	_, err := f.service.RecordMeasurementBatch(ctx, caseID, contracts.RecordMeasurementBatchRequest{
		BatchID:  "late-batch",
		Readings: []contracts.MeasurementReadingInput{reading(25.2, "Rui", "")},
	})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("error = %v, want ErrStateConflict", err)
	}
}

func TestBatchUnknownCase(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.RecordMeasurementBatch(context.Background(), "case-missing", contracts.RecordMeasurementBatchRequest{
		BatchID:  "b",
		Readings: []contracts.MeasurementReadingInput{reading(1, "Rui", "")},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestBatchFactsMatchSingleSubmission(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	singleCase := f.openCase(t, "MET-B9A")
	batchCase := f.openCase(t, "MET-B9B")

	single, err := f.service.RecordMeasurement(ctx, singleCase, contracts.RecordMeasurementRequest{Observed: 25.4, Technician: "Rui"})
	if err != nil {
		t.Fatalf("RecordMeasurement: %v", err)
	}
	batch, err := f.service.RecordMeasurementBatch(ctx, batchCase, contracts.RecordMeasurementBatchRequest{
		BatchID:  "parity",
		Readings: []contracts.MeasurementReadingInput{reading(25.4, "Rui", "")},
	})
	if err != nil {
		t.Fatalf("RecordMeasurementBatch: %v", err)
	}
	want := single.Measurements[0]
	got := batch.Results[0].Measurement
	if got.Observed != want.Observed || got.Nominal != want.Nominal || got.Tolerance != want.Tolerance ||
		got.Deviation != want.Deviation || got.Accepted != want.Accepted || got.Technician != want.Technician {
		t.Fatalf("batch fact %+v does not match single fact %+v", got, want)
	}
	if batch.Case.State != single.Case.State {
		t.Fatalf("batch case state %q != single %q", batch.Case.State, single.Case.State)
	}
}

func TestBatchConcurrentSameKeyCommitsOnce(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B10")
	ctx := context.Background()
	req := contracts.RecordMeasurementBatchRequest{
		BatchID: "race-key",
		Readings: []contracts.MeasurementReadingInput{
			reading(25.1, "Rui", "p1"),
			reading(24.9, "Rui", "p2"),
		},
	}

	const goroutines = 12
	var wg sync.WaitGroup
	type outcome struct {
		replayed bool
		err      error
	}
	outcomes := make([]outcome, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			view, err := f.service.RecordMeasurementBatch(ctx, caseID, req)
			if err == nil {
				outcomes[i] = outcome{replayed: view.Replayed}
			} else {
				outcomes[i] = outcome{err: err}
			}
		}(i)
	}
	wg.Wait()

	committed, replayed := 0, 0
	for _, item := range outcomes {
		if item.err != nil {
			t.Fatalf("concurrent identical submit errored: %v", item.err)
		}
		if item.replayed {
			replayed++
		} else {
			committed++
		}
	}
	if committed != 1 || replayed != goroutines-1 {
		t.Fatalf("committed=%d replayed=%d, want 1 and %d", committed, replayed, goroutines-1)
	}
	view, err := f.service.Case(ctx, caseID)
	if err != nil {
		t.Fatalf("Case: %v", err)
	}
	if len(view.Measurements) != 2 {
		t.Fatalf("race created %d measurements, want exactly 2", len(view.Measurements))
	}
}

func TestBatchConcurrentSameKeyDivergentPayloads(t *testing.T) {
	f := newFixture(t)
	caseID := f.openCase(t, "MET-B11")
	ctx := context.Background()

	payload := func(tag string) contracts.RecordMeasurementBatchRequest {
		return contracts.RecordMeasurementBatchRequest{
			BatchID: "race-key",
			Readings: []contracts.MeasurementReadingInput{
				reading(25.1, "Rui", tag),
				reading(24.9, "Rui", tag+"-2"),
			},
		}
	}

	const goroutines = 8
	var wg sync.WaitGroup
	results := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.service.RecordMeasurementBatch(ctx, caseID, payload(fmt.Sprintf("g%d", i)))
			results[i] = err
		}(i)
	}
	wg.Wait()

	commits, replays := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			commits++
		case errors.Is(err, domain.ErrBatchReplayed):
			replays++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if commits != 1 || replays != goroutines-1 {
		t.Fatalf("commits=%d conflicts=%d, want 1 and %d", commits, replays, goroutines-1)
	}
	view, err := f.service.Case(ctx, caseID)
	if err != nil {
		t.Fatalf("Case: %v", err)
	}
	if len(view.Measurements) != 2 {
		t.Fatalf("divergent race left %d measurements, want exactly 2", len(view.Measurements))
	}
}
