package assurance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

// ItemError identifies one reading inside a batch that could not form a valid
// measurement. Index is the zero-based position in the request payload.
type ItemError struct {
	Index   int
	Code    string
	Message string
}

// BatchItemsError marks a whole batch invalid because one or more readings
// failed validation. It unwraps to ErrInvalidInput so the HTTP layer maps it to
// 400, and carries the per-reading failures for a locatable response.
type BatchItemsError struct {
	Items []ItemError
}

func (e *BatchItemsError) Error() string {
	if len(e.Items) == 0 {
		return "batch contained invalid readings"
	}
	return fmt.Sprintf("batch contained %d invalid reading(s)", len(e.Items))
}

func (e *BatchItemsError) Unwrap() error {
	return domain.ErrInvalidInput
}

// RecordMeasurements submits a batch of readings for one case. The whole batch
// is validated before anything is committed, so no case, measurement, or audit
// state is left behind when any reading is invalid. A client-supplied
// idempotency key makes retries replay the original result instead of writing
// duplicates.
func (s *Service) RecordMeasurements(ctx context.Context, caseID string, input contracts.RecordMeasurementBatchRequest) (contracts.BatchMeasurementsResponse, bool, error) {
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return contracts.BatchMeasurementsResponse{}, false, fmt.Errorf("%w: idempotency_key is required", domain.ErrInvalidInput)
	}
	if len(input.Readings) == 0 {
		return contracts.BatchMeasurementsResponse{}, false, fmt.Errorf("%w: at least one reading is required", domain.ErrInvalidInput)
	}
	fingerprint := readingsFingerprint(input.Readings)

	var result contracts.BatchMeasurementsResponse
	replayed := false
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}

		if existing, exists := m.MeasurementBatch(item.ID, key); exists {
			if existing.Fingerprint != fingerprint {
				return fmt.Errorf("%w: key %s for case %s", domain.ErrIdempotencyKeyReuse, key, item.ID)
			}
			// Retry of the original submission: replay the stored facts
			// without writing anything again, regardless of whether the case
			// has since moved to review_ready, recheck_required, or sealed.
			result = batchReplayResponse(m, item, existing)
			replayed = true
			return nil
		}

		if item.State != domain.CaseAwaitingMeasurement {
			return fmt.Errorf("%w: case is not awaiting measurement", domain.ErrStateConflict)
		}

		at := s.now()
		measurements := make([]domain.Measurement, len(input.Readings))
		events := make([]domain.AuditEvent, len(input.Readings))
		accepted := make([]bool, len(input.Readings))
		var itemErrors []ItemError
		for index, reading := range input.Readings {
			measurement, err := domain.NewMeasurement(
				m.NextID("measurement"),
				item.ID,
				reading.Observed,
				item.Nominal,
				item.Tolerance,
				reading.Technician,
				at,
			)
			if err != nil {
				itemErrors = append(itemErrors, ItemError{
					Index:   index,
					Code:    "invalid_input",
					Message: err.Error(),
				})
				continue
			}
			kind, detail := measurementEventDescription(measurement.Accepted)
			event, err := domain.NewAuditEvent(
				m.NextID("event"),
				"case",
				item.ID,
				item.ID,
				kind,
				measurement.Technician,
				detail,
				at,
			)
			if err != nil {
				itemErrors = append(itemErrors, ItemError{
					Index:   index,
					Code:    "invalid_input",
					Message: err.Error(),
				})
				continue
			}
			measurements[index] = measurement
			events[index] = event
			accepted[index] = measurement.Accepted
		}
		if len(itemErrors) > 0 {
			// Returning an error rolls back the entire cloned mutation,
			// including the ID counters: no half-batch survives.
			return &BatchItemsError{Items: itemErrors}
		}

		if err := item.RecordMeasurementBatch(accepted, at); err != nil {
			return err
		}

		measurementIDs := make([]string, len(measurements))
		eventIDs := make([]string, len(events))
		results := make([]contracts.MeasurementItemResult, len(measurements))
		for index := range measurements {
			if err := m.AppendMeasurement(measurements[index]); err != nil {
				return err
			}
			m.AppendEvent(events[index])
			measurementIDs[index] = measurements[index].ID
			eventIDs[index] = events[index].ID
			results[index] = contracts.MeasurementItemResult{
				Index:       index,
				Measurement: measurements[index],
				EventID:     events[index].ID,
			}
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}

		batch, err := domain.NewMeasurementBatch(
			m.NextID("batch"),
			item.ID,
			key,
			fingerprint,
			measurementIDs,
			eventIDs,
			at,
		)
		if err != nil {
			return err
		}
		if err := m.PutMeasurementBatch(batch); err != nil {
			return err
		}

		result = contracts.BatchMeasurementsResponse{
			CaseResponse:   caseResponse(m, item),
			BatchID:        batch.ID,
			IdempotencyKey: batch.IdempotencyKey,
			Replayed:       false,
			Results:        results,
		}
		return nil
	})
	return result, replayed, err
}

func batchReplayResponse(m *repository.Mutation, item domain.VerificationCase, batch domain.MeasurementBatch) contracts.BatchMeasurementsResponse {
	byID := make(map[string]domain.Measurement, len(batch.MeasurementIDs))
	for _, measurement := range m.Measurements(item.ID) {
		byID[measurement.ID] = measurement
	}
	results := make([]contracts.MeasurementItemResult, len(batch.MeasurementIDs))
	for index, measurementID := range batch.MeasurementIDs {
		results[index] = contracts.MeasurementItemResult{
			Index:       index,
			Measurement: byID[measurementID],
			EventID:     batch.EventIDs[index],
		}
	}
	return contracts.BatchMeasurementsResponse{
		CaseResponse:   caseResponse(m, item),
		BatchID:        batch.ID,
		IdempotencyKey: batch.IdempotencyKey,
		Replayed:       true,
		Results:        results,
	}
}

// readingsFingerprint produces a stable digest of the batch payload. A retry
// with the same key must carry the same readings in the same order.
func readingsFingerprint(readings []contracts.RecordMeasurementRequest) string {
	var builder strings.Builder
	for _, reading := range readings {
		builder.WriteString(strconv.FormatFloat(reading.Observed, 'g', -1, 64))
		builder.WriteByte(0)
		builder.WriteString(strings.TrimSpace(reading.Technician))
		builder.WriteByte(0)
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:])
}
