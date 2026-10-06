package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
)

func TestBatchPutAndLookup(t *testing.T) {
	repo := New()
	ctx := context.Background()
	record := sampleBatchRecord(t, "case-1", "batch-1")

	if err := repo.Mutate(ctx, func(m *Mutation) error {
		putCaseForTest(t, m, "case-1", "device-1")
		return m.PutMeasurementBatch(record)
	}); err != nil {
		t.Fatalf("PutMeasurementBatch: %v", err)
	}

	if err := repo.Read(ctx, func(snapshot Snapshot) error {
		stored, ok := snapshot.MeasurementBatch("case-1", "batch-1")
		if !ok {
			t.Fatal("committed batch is not readable")
		}
		if stored.ID != "batch-1" || len(stored.MeasurementIDs) != 2 {
			t.Fatalf("stored record = %+v", stored)
		}
		if _, ok := snapshot.MeasurementBatch("case-1", "other"); ok {
			t.Fatal("unknown batch id reported as found")
		}
		if _, ok := snapshot.MeasurementBatch("case-other", "batch-1"); ok {
			t.Fatal("batch id leaked across cases")
		}
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	// The same batch id on the same case cannot be stored twice.
	err := repo.Mutate(ctx, func(m *Mutation) error {
		return m.PutMeasurementBatch(record)
	})
	if !errors.Is(err, domain.ErrBatchReplayed) {
		t.Fatalf("duplicate put error = %v, want ErrBatchReplayed", err)
	}
}

func TestBatchPutRequiresCase(t *testing.T) {
	repo := New()
	ctx := context.Background()
	err := repo.Mutate(ctx, func(m *Mutation) error {
		return m.PutMeasurementBatch(sampleBatchRecord(t, "missing-case", "batch-1"))
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestFailedMutationDoesNotCommitBatchOrCounters(t *testing.T) {
	repo := New()
	ctx := context.Background()
	if err := repo.Mutate(ctx, func(m *Mutation) error {
		putCaseForTest(t, m, "case-1", "device-1")
		_ = m.NextID("measurement")
		_ = m.PutMeasurementBatch(sampleBatchRecord(t, "case-1", "batch-1"))
		return errors.New("abort")
	}); err == nil || err.Error() != "abort" {
		t.Fatalf("expected abort error, got %v", err)
	}

	if err := repo.Read(ctx, func(snapshot Snapshot) error {
		if _, exists := snapshot.Case("case-1"); exists {
			t.Fatal("aborted mutation committed the case")
		}
		if _, exists := snapshot.MeasurementBatch("case-1", "batch-1"); exists {
			t.Fatal("aborted mutation committed the batch record")
		}
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Counters also rolled back: ids restart from 0001 on the next mutation.
	var nextID string
	if err := repo.Mutate(ctx, func(m *Mutation) error {
		putCaseForTest(t, m, "case-1", "device-1")
		nextID = m.NextID("measurement")
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if want := "measurement-0001"; nextID != want {
		t.Fatalf("next id after rollback = %q, want %q", nextID, want)
	}
}

func putCaseForTest(t *testing.T, m *Mutation, caseID, deviceID string) {
	t.Helper()
	if _, exists := m.Case(caseID); exists {
		return
	}
	item, err := domain.NewVerificationCase(caseID, deviceID, 25.0, 0.5, "Lin", time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewVerificationCase: %v", err)
	}
	if err := m.PutCase(item); err != nil {
		t.Fatalf("PutCase: %v", err)
	}
}

func sampleBatchRecord(t *testing.T, caseID, batchID string) domain.MeasurementBatchRecord {
	t.Helper()
	item, err := domain.NewVerificationCase(caseID, "device-1", 25.0, 0.5, "Lin", time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewVerificationCase: %v", err)
	}
	readings := []domain.BatchReading{
		{Observed: 25.1, Technician: "Rui", Ref: "p1"},
		{Observed: 24.8, Technician: "Rui"},
	}
	record, err := domain.NewMeasurementBatchRecord(
		batchID,
		caseID,
		readings,
		[]string{"measurement-0001", "measurement-0002"},
		domain.BatchRecordView{Case: item, Measurements: nil, Events: nil},
		time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewMeasurementBatchRecord: %v", err)
	}
	return record
}
