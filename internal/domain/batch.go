package domain

import (
	"fmt"
	"strings"
	"time"
)

// MeasurementBatch is the persisted record of one batch submission. It lets a
// retried submission with the same idempotency key be replayed instead of
// writing the measurements and audit events a second time.
type MeasurementBatch struct {
	ID             string    `json:"id"`
	CaseID         string    `json:"case_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Fingerprint    string    `json:"fingerprint"`
	MeasurementIDs []string  `json:"measurement_ids"`
	EventIDs       []string  `json:"event_ids"`
	CreatedAt      time.Time `json:"created_at"`
}

func NewMeasurementBatch(id, caseID, key, fingerprint string, measurementIDs, eventIDs []string, at time.Time) (MeasurementBatch, error) {
	item := MeasurementBatch{
		ID:             strings.TrimSpace(id),
		CaseID:         strings.TrimSpace(caseID),
		IdempotencyKey: strings.TrimSpace(key),
		Fingerprint:    strings.TrimSpace(fingerprint),
		MeasurementIDs: append([]string(nil), measurementIDs...),
		EventIDs:       append([]string(nil), eventIDs...),
		CreatedAt:      at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return MeasurementBatch{}, err
	}
	return item, nil
}

func (b MeasurementBatch) Validate() error {
	if b.ID == "" || b.CaseID == "" || b.IdempotencyKey == "" || b.Fingerprint == "" {
		return fmt.Errorf("%w: batch id, case id, idempotency key, and fingerprint are required", ErrInvalidInput)
	}
	if len(b.MeasurementIDs) == 0 || len(b.MeasurementIDs) != len(b.EventIDs) {
		return fmt.Errorf("%w: batch must record one measurement and event per reading", ErrInvalidInput)
	}
	if b.CreatedAt.IsZero() {
		return fmt.Errorf("%w: batch time is required", ErrInvalidInput)
	}
	return nil
}
