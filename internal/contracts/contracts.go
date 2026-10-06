package contracts

import "example.com/solo-0021-metrology-assurance-suite/internal/domain"

type RegisterDeviceRequest struct {
	AssetTag string `json:"asset_tag"`
	Model    string `json:"model"`
	Room     string `json:"room"`
	Steward  string `json:"steward"`
}

type OpenCaseRequest struct {
	DeviceID  string  `json:"device_id"`
	Nominal   float64 `json:"nominal"`
	Tolerance float64 `json:"tolerance"`
	Steward   string  `json:"steward"`
}

type RecordMeasurementRequest struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
}

// RecordMeasurementBatchRequest submits one atomic batch of readings for a
// case. BatchID is the caller-chosen idempotency key for the batch.
type RecordMeasurementBatchRequest struct {
	BatchID  string                    `json:"batch_id"`
	Readings []MeasurementReadingInput `json:"readings"`
}

type MeasurementReadingInput struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
	ClientRef  string  `json:"client_ref"`
}

type ReopenCaseRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

type SealCaseRequest struct {
	Reviewer string `json:"reviewer"`
	Summary  string `json:"summary"`
}

type DeviceResponse struct {
	Device domain.Device `json:"device"`
}

type CaseResponse struct {
	Case         domain.VerificationCase `json:"case"`
	Measurements []domain.Measurement    `json:"measurements"`
	Seal         *domain.EvidenceSeal    `json:"seal,omitempty"`
	Events       []domain.AuditEvent     `json:"events"`
}

// MeasurementResultItem locates one committed measurement fact inside a batch
// result. Index is the zero-based position in the submitted readings; ClientRef
// echoes the optional token the caller supplied.
type MeasurementResultItem struct {
	Index       int                `json:"index"`
	ClientRef   string             `json:"client_ref,omitempty"`
	Measurement domain.Measurement `json:"measurement"`
}

// BatchMeasurementsResponse is the per-reading result of a batch submission.
// Replayed is true when the response was served from a stored batch with the
// same idempotency key rather than a new commit.
type BatchMeasurementsResponse struct {
	BatchID      string                  `json:"batch_id"`
	Replayed     bool                    `json:"replayed"`
	Results      []MeasurementResultItem `json:"results"`
	Case         domain.VerificationCase `json:"case"`
	Measurements []domain.Measurement    `json:"measurements"`
	Seal         *domain.EvidenceSeal    `json:"seal,omitempty"`
	Events       []domain.AuditEvent     `json:"events"`
}

type EventsResponse struct {
	Events []domain.AuditEvent `json:"events"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
