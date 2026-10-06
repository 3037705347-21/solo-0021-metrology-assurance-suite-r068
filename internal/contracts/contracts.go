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

type RecordMeasurementBatchRequest struct {
	IdempotencyKey string                     `json:"idempotency_key"`
	Readings       []RecordMeasurementRequest `json:"readings"`
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

// MeasurementItemResult is the locatable outcome of one reading inside a
// batch. Index is the reading's zero-based position in the request.
type MeasurementItemResult struct {
	Index       int                `json:"index"`
	Measurement domain.Measurement `json:"measurement"`
	EventID     string             `json:"event_id"`
}

type BatchMeasurementsResponse struct {
	CaseResponse
	BatchID        string                  `json:"batch_id"`
	IdempotencyKey string                  `json:"idempotency_key"`
	Replayed       bool                    `json:"replayed"`
	Results        []MeasurementItemResult `json:"results"`
}

type EventsResponse struct {
	Events []domain.AuditEvent `json:"events"`
}

type ItemErrorDetail struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details []ItemErrorDetail `json:"details,omitempty"`
}
