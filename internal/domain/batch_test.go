package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestRecordMeasurementBatchTransition(t *testing.T) {
	at := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)

	t.Run("all accepted moves to review ready once", func(t *testing.T) {
		c := awaitingCase(t)
		if err := c.RecordMeasurementBatch([]bool{true, true, true}, at); err != nil {
			t.Fatalf("RecordMeasurementBatch: %v", err)
		}
		if c.State != CaseReviewReady {
			t.Fatalf("state = %q, want review_ready", c.State)
		}
		if c.Version != 2 {
			t.Fatalf("version = %d, want 2 (exactly one transition)", c.Version)
		}
		if !c.UpdatedAt.Equal(at) {
			t.Fatalf("updated at = %v, want %v", c.UpdatedAt, at)
		}
	})

	t.Run("one rejected reading requires recheck", func(t *testing.T) {
		c := awaitingCase(t)
		if err := c.RecordMeasurementBatch([]bool{true, false, true}, at); err != nil {
			t.Fatalf("RecordMeasurementBatch: %v", err)
		}
		if c.State != CaseRecheckRequired {
			t.Fatalf("state = %q, want recheck_required", c.State)
		}
		if c.Version != 2 {
			t.Fatalf("version = %d, want 2", c.Version)
		}
	})

	t.Run("non-awaiting state is rejected", func(t *testing.T) {
		c := awaitingCase(t)
		if err := c.RecordMeasurementBatch([]bool{true}, at); err != nil {
			t.Fatalf("RecordMeasurementBatch: %v", err)
		}
		err := c.RecordMeasurementBatch([]bool{true}, at)
		if !errors.Is(err, ErrStateConflict) {
			t.Fatalf("second batch error = %v, want ErrStateConflict", err)
		}
		if c.Version != 2 {
			t.Fatalf("failed batch advanced version to %d", c.Version)
		}
	})

	t.Run("empty batch is invalid", func(t *testing.T) {
		c := awaitingCase(t)
		err := c.RecordMeasurementBatch(nil, at)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput", err)
		}
	})
}

func TestMeasurementBatchRecordValidation(t *testing.T) {
	at := time.Now().UTC()
	readings := []BatchReading{{Observed: 1, Technician: "Rui"}, {Observed: 2, Technician: "Rui"}}
	ids := []string{"measurement-0001", "measurement-0002"}
	view := BatchRecordView{
		Case:         awaitingCase(t),
		Measurements: []Measurement{{ID: "measurement-0001"}},
		Events:       []AuditEvent{{ID: "event-0001"}},
	}

	t.Run("valid record", func(t *testing.T) {
		record, err := NewMeasurementBatchRecord(" batch-1 ", " case-1 ", readings, ids, view, at)
		if err != nil {
			t.Fatalf("NewMeasurementBatchRecord: %v", err)
		}
		if record.ID != "batch-1" || record.CaseID != "case-1" {
			t.Fatalf("identity was not trimmed: %+v", record)
		}
		id, ok := record.MeasurementIDFor(1)
		if !ok || id != "measurement-0002" {
			t.Fatalf("MeasurementIDFor(1) = %q, %v", id, ok)
		}
		if _, ok := record.MeasurementIDFor(2); ok {
			t.Fatal("out-of-range index was reported as found")
		}
	})

	t.Run("missing identity", func(t *testing.T) {
		if _, err := NewMeasurementBatchRecord("", "case-1", readings, ids, view, at); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("empty batch id error = %v", err)
		}
	})

	t.Run("count mismatch", func(t *testing.T) {
		if _, err := NewMeasurementBatchRecord("b", "case-1", readings, []string{"measurement-0001"}, view, at); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("count mismatch error = %v", err)
		}
	})

	t.Run("oversized batch", func(t *testing.T) {
		tooMany := make([]BatchReading, MaxBatchReadings+1)
		tooManyIDs := make([]string, MaxBatchReadings+1)
		for i := range tooMany {
			tooMany[i] = BatchReading{Observed: 1, Technician: "Rui"}
			tooManyIDs[i] = "x"
		}
		if _, err := NewMeasurementBatchRecord("b", "c", tooMany, tooManyIDs, view, at); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("oversized batch error = %v", err)
		}
	})
}

func TestBatchMeasurementFactsMatchSingleSubmission(t *testing.T) {
	at := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	single, err := NewMeasurement("measurement-0007", "case-1", math.Pi, 3.0, 0.2, "Rui", at)
	if err != nil {
		t.Fatalf("NewMeasurement: %v", err)
	}
	batched, err := NewMeasurement("measurement-0008", "case-1", math.Pi, 3.0, 0.2, "Rui", at)
	if err != nil {
		t.Fatalf("NewMeasurement: %v", err)
	}
	if single.Observed != batched.Observed ||
		single.Nominal != batched.Nominal ||
		single.Tolerance != batched.Tolerance ||
		single.Deviation != batched.Deviation ||
		single.Accepted != batched.Accepted ||
		single.Technician != batched.Technician {
		t.Fatalf("batch fact %+v does not match single fact %+v", batched, single)
	}
}

func awaitingCase(t *testing.T) VerificationCase {
	t.Helper()
	item, err := NewVerificationCase("case-1", "device-1", 25.0, 0.5, "Lin", time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewVerificationCase: %v", err)
	}
	return item
}
