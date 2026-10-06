package domain

import (
	"fmt"
	"strings"
	"time"
)

// MaxBatchReadings bounds a single measurement batch so validation and the
// atomic commit stay small and predictable.
const MaxBatchReadings = 100

// BatchReading is one reading supplied by the caller inside a batch. Ref is an
// optional caller-chosen correlation token used to locate the per-reading
// result; when present it must be unique within the batch.
type BatchReading struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
	Ref        string  `json:"client_ref,omitempty"`
}

// BatchRecordView is the immutable case projection captured at the moment a
// batch committed. It lets an idempotent replay return the exact results and
// case view the caller received on the first attempt, even if the case later
// moved on (reopen, another measurement cycle, sealing).
type BatchRecordView struct {
	Case         VerificationCase `json:"case"`
	Measurements []Measurement    `json:"measurements"`
	Events       []AuditEvent     `json:"events"`
}

// MeasurementBatchRecord is the stored result of one accepted batch
// submission. It is keyed by (case id, batch id) so retries of the same batch
// replay instead of creating measurements twice.
type MeasurementBatchRecord struct {
	ID             string          `json:"id"`
	CaseID         string          `json:"case_id"`
	Readings       []BatchReading  `json:"readings"`
	MeasurementIDs []string        `json:"measurement_ids"`
	View           BatchRecordView `json:"view"`
	CreatedAt      time.Time       `json:"created_at"`
}

func NewMeasurementBatchRecord(
	id, caseID string,
	readings []BatchReading,
	measurementIDs []string,
	view BatchRecordView,
	at time.Time,
) (MeasurementBatchRecord, error) {
	item := MeasurementBatchRecord{
		ID:             NormalizeBatchID(id),
		CaseID:         strings.TrimSpace(caseID),
		Readings:       readings,
		MeasurementIDs: measurementIDs,
		View:           view,
		CreatedAt:      at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return MeasurementBatchRecord{}, err
	}
	return item, nil
}

func (b MeasurementBatchRecord) Validate() error {
	if b.ID == "" || b.CaseID == "" {
		return fmt.Errorf("%w: batch id and case id are required", ErrInvalidInput)
	}
	if len(b.Readings) < 1 || len(b.Readings) > MaxBatchReadings {
		return fmt.Errorf("%w: a batch must contain between 1 and %d readings", ErrInvalidInput, MaxBatchReadings)
	}
	if len(b.MeasurementIDs) != len(b.Readings) {
		return fmt.Errorf("%w: batch result count must match reading count", ErrInvalidInput)
	}
	for _, id := range b.MeasurementIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: batch measurement ids are required", ErrInvalidInput)
		}
	}
	if b.CreatedAt.IsZero() {
		return fmt.Errorf("%w: batch time is required", ErrInvalidInput)
	}
	return nil
}

// MeasurementIDFor returns the measurement created for the reading at index.
func (b MeasurementBatchRecord) MeasurementIDFor(index int) (string, bool) {
	if index < 0 || index >= len(b.MeasurementIDs) {
		return "", false
	}
	return b.MeasurementIDs[index], true
}

// NormalizeBatchID trims the caller-supplied idempotency key. It is a
// correlation token, so its original casing is preserved.
func NormalizeBatchID(value string) string {
	return strings.TrimSpace(value)
}

// NormalizeReadingRef trims an optional per-reading correlation token.
func NormalizeReadingRef(value string) string {
	return strings.TrimSpace(value)
}
