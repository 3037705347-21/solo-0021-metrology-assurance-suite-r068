package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"example.com/solo-0021-metrology-assurance-suite/internal/assurance"
	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
)

const maxRequestBytes = 1 << 20

func decodeJSON[T any](writer http.ResponseWriter, request *http.Request) (T, error) {
	var input T
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("%w: invalid JSON request: %v", domain.ErrInvalidInput, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return input, fmt.Errorf("%w: request body must contain one JSON value", domain.ErrInvalidInput)
	}
	return input, nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, err error) {
	status, code := errorStatus(err)
	message := err.Error()
	if status == http.StatusInternalServerError {
		message = "unexpected server failure"
	}
	body := contracts.ErrorBody{
		Code:    code,
		Message: message,
	}
	var batchItems *assurance.BatchItemsError
	if errors.As(err, &batchItems) {
		body.Details = make([]contracts.ItemErrorDetail, len(batchItems.Items))
		for index, item := range batchItems.Items {
			body.Details[index] = contracts.ItemErrorDetail{
				Index:   item.Index,
				Code:    item.Code,
				Message: item.Message,
			}
		}
	}
	writeJSON(writer, status, contracts.ErrorResponse{Error: body})
}

func errorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_input"
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrDuplicateAssetTag):
		return http.StatusConflict, "duplicate_asset_tag"
	case errors.Is(err, domain.ErrActiveCaseExists):
		return http.StatusConflict, "active_case_exists"
	case errors.Is(err, domain.ErrIdempotencyKeyReuse):
		return http.StatusConflict, "idempotency_key_reuse"
	case errors.Is(err, domain.ErrSealAlreadyCreated):
		return http.StatusConflict, "seal_exists"
	case errors.Is(err, domain.ErrStateConflict):
		return http.StatusConflict, "state_conflict"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}
