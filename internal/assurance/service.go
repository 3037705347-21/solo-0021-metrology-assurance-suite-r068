package assurance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			input.Tolerance,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			"verification case opened",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponseFromSnapshot(snapshot, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		stamp := s.now()
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			input.Observed,
			item.Nominal,
			item.Tolerance,
			input.Technician,
			stamp,
		)
		if err != nil {
			return err
		}
		if err := item.RecordMeasurement(measurement.Accepted, stamp); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := measurementEvent(m.NextID("event"), item.ID, measurement, stamp)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

// RecordMeasurementBatch commits every reading of a case-scoped batch in one
// repository mutation: all readings are validated against the current case
// state before anything is written, so a single invalid or inapplicable
// reading rejects the whole request without leaving measurements, case state,
// or audit events half committed. BatchID is an idempotency key scoped to the
// case; replaying a stored batch returns the original per-reading results
// instead of creating duplicates.
func (s *Service) RecordMeasurementBatch(ctx context.Context, caseID string, input contracts.RecordMeasurementBatchRequest) (contracts.BatchMeasurementsResponse, error) {
	var result contracts.BatchMeasurementsResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		batchID := domain.NormalizeBatchID(input.BatchID)
		if batchID == "" {
			return fmt.Errorf("%w: batch_id is required", domain.ErrInvalidInput)
		}
		readings, err := normalizeReadings(input.Readings)
		if err != nil {
			return err
		}
		if stored, exists := m.MeasurementBatch(item.ID, batchID); exists {
			if !sameBatchPayload(readings, stored.Readings) {
				return fmt.Errorf("%w: batch %s was already submitted to case %s with different readings", domain.ErrBatchReplayed, batchID, item.ID)
			}
			result = batchResponse(stored, true)
			return nil
		}
		if item.State != domain.CaseAwaitingMeasurement {
			return fmt.Errorf("%w: case is not awaiting measurement", domain.ErrStateConflict)
		}

		stamp := s.now()
		measurements := make([]domain.Measurement, len(readings))
		accepted := make([]bool, len(readings))
		for index, reading := range readings {
			measurement, err := domain.NewMeasurement(
				m.NextID("measurement"),
				item.ID,
				reading.Observed,
				item.Nominal,
				item.Tolerance,
				reading.Technician,
				stamp,
			)
			if err != nil {
				return readingInputError(index, reading.Ref, err)
			}
			measurements[index] = measurement
			accepted[index] = measurement.Accepted
		}

		if err := item.RecordMeasurementBatch(accepted, stamp); err != nil {
			return err
		}
		for _, measurement := range measurements {
			if err := m.AppendMeasurement(measurement); err != nil {
				return err
			}
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		for _, measurement := range measurements {
			event, err := measurementEvent(m.NextID("event"), item.ID, measurement, stamp)
			if err != nil {
				return err
			}
			m.AppendEvent(event)
		}
		batchEvent, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"measurement_batch_recorded",
			readings[0].Technician,
			fmt.Sprintf("measurement batch %s recorded %d readings; case is now %s", batchID, len(readings), item.State),
			stamp,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(batchEvent)

		measurementIDs := make([]string, len(measurements))
		for index, measurement := range measurements {
			measurementIDs[index] = measurement.ID
		}
		view := domain.BatchRecordView{
			Case:         item,
			Measurements: m.Measurements(item.ID),
			Events:       m.Events(item.ID),
		}
		record, err := domain.NewMeasurementBatchRecord(batchID, item.ID, readings, measurementIDs, view, stamp)
		if err != nil {
			return err
		}
		if err := m.PutMeasurementBatch(record); err != nil {
			return err
		}
		result = batchResponse(record, false)
		return nil
	})
	return result, err
}

func normalizeReadings(input []contracts.MeasurementReadingInput) ([]domain.BatchReading, error) {
	if len(input) < 1 || len(input) > domain.MaxBatchReadings {
		return nil, fmt.Errorf("%w: a batch must contain between 1 and %d readings", domain.ErrInvalidInput, domain.MaxBatchReadings)
	}
	readings := make([]domain.BatchReading, len(input))
	refs := make(map[string]int, len(input))
	for index, raw := range input {
		reading := domain.BatchReading{
			Observed:   raw.Observed,
			Technician: strings.TrimSpace(raw.Technician),
			Ref:        domain.NormalizeReadingRef(raw.ClientRef),
		}
		if reading.Technician == "" {
			return nil, fmt.Errorf("%w: reading at index %d is missing a technician", domain.ErrInvalidInput, index)
		}
		if reading.Ref != "" {
			if first, exists := refs[reading.Ref]; exists {
				return nil, fmt.Errorf("%w: client_ref %q repeats (first used at index %d, again at index %d)", domain.ErrInvalidInput, reading.Ref, first, index)
			}
			refs[reading.Ref] = index
		}
		readings[index] = reading
	}
	return readings, nil
}

func readingInputError(index int, ref string, cause error) error {
	detail := strings.TrimPrefix(cause.Error(), domain.ErrInvalidInput.Error()+": ")
	location := fmt.Sprintf("index %d", index)
	if ref != "" {
		location = fmt.Sprintf("index %d (client_ref %q)", index, ref)
	}
	return fmt.Errorf("%w: reading at %s: %s", domain.ErrInvalidInput, location, detail)
}

func sameBatchPayload(got, want []domain.BatchReading) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index].Observed != want[index].Observed ||
			got[index].Technician != want[index].Technician ||
			got[index].Ref != want[index].Ref {
			return false
		}
	}
	return true
}

func measurementEvent(eventID, caseID string, measurement domain.Measurement, at time.Time) (domain.AuditEvent, error) {
	kind := "measurement_rejected"
	detail := "measurement exceeded tolerance and requires another measurement"
	if measurement.Accepted {
		kind = "measurement_accepted"
		detail = "measurement is within tolerance and ready for review"
	}
	return domain.NewAuditEvent(
		eventID,
		"case",
		caseID,
		caseID,
		kind,
		measurement.Technician,
		detail,
		at,
	)
}

func batchResponse(record domain.MeasurementBatchRecord, replayed bool) contracts.BatchMeasurementsResponse {
	byID := make(map[string]domain.Measurement, len(record.View.Measurements))
	for _, measurement := range record.View.Measurements {
		byID[measurement.ID] = measurement
	}
	results := make([]contracts.MeasurementResultItem, len(record.Readings))
	for index, reading := range record.Readings {
		results[index] = contracts.MeasurementResultItem{
			Index:       index,
			ClientRef:   reading.Ref,
			Measurement: byID[record.MeasurementIDs[index]],
		}
	}
	response := contracts.BatchMeasurementsResponse{
		BatchID:      record.ID,
		Replayed:     replayed,
		Results:      results,
		Case:         record.View.Case,
		Measurements: copyMeasurements(record.View.Measurements),
		Events:       copyEvents(record.View.Events),
	}
	return response
}

func copyMeasurements(values []domain.Measurement) []domain.Measurement {
	result := make([]domain.Measurement, len(values))
	copy(result, values)
	return result
}

func copyEvents(values []domain.AuditEvent) []domain.AuditEvent {
	result := make([]domain.AuditEvent, len(values))
	copy(result, values)
	return result
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if err := item.Reopen(s.now()); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_reopened",
			actor,
			reason,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		if err := item.Seal(s.now()); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			input.Reviewer,
			input.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_sealed",
			seal.Reviewer,
			seal.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: m.Measurements(item.ID),
		Events:       m.Events(item.ID),
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}
