package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type historyCursor struct {
	Environment string `json:"e"`
	Server      string `json:"s"`
	CreatedAt   int64  `json:"t"`
	ID          string `json:"i"`
}

func (h *handler) ListOperations(ctx context.Context, request *connect.Request[panelv1.ListOperationsRequest]) (*connect.Response[panelv1.ListOperationsResponse], error) {
	position, size, err := parseHistoryPage(request.Msg)
	if err != nil {
		return nil, err
	}
	queries := store.New(h.database)
	var rows []store.OperationHistory
	switch {
	case position.Environment != "" && position.Server != "":
		rows, err = queries.ListOperationHistoryForEnvironmentServer(ctx, store.ListOperationHistoryForEnvironmentServerParams{EnvironmentID: position.Environment, ServerID: position.Server, CursorTime: position.CreatedAt, CursorID: position.ID, PageLimit: size + 1})
	case position.Environment != "":
		rows, err = queries.ListOperationHistoryForEnvironment(ctx, store.ListOperationHistoryForEnvironmentParams{EnvironmentID: position.Environment, CursorTime: position.CreatedAt, CursorID: position.ID, PageLimit: size + 1})
	case position.Server != "":
		rows, err = queries.ListOperationHistoryForServer(ctx, store.ListOperationHistoryForServerParams{ServerID: position.Server, CursorTime: position.CreatedAt, CursorID: position.ID, PageLimit: size + 1})
	default:
		rows, err = queries.ListOperationHistory(ctx, store.ListOperationHistoryParams{CursorTime: position.CreatedAt, CursorID: position.ID, PageLimit: size + 1})
	}
	if err != nil {
		return nil, metadataError(err)
	}
	response := &panelv1.ListOperationsResponse{Operations: make([]*panelv1.Operation, 0, min(int64(len(rows)), size)), Page: &panelv1.PageResponse{}}
	if int64(len(rows)) > size {
		last := rows[size-1]
		position.CreatedAt, position.ID = last.CreatedAt, last.ID
		encoded, err := json.Marshal(position)
		if err != nil {
			return nil, metadataError(err)
		}
		response.Page.NextPageToken = base64.RawURLEncoding.EncodeToString(encoded)
		rows = rows[:size]
	}
	for _, value := range rows {
		response.Operations = append(response.Operations, historyOperation(value))
	}
	return connect.NewResponse(response), nil
}

func parseHistoryPage(request *panelv1.ListOperationsRequest) (historyCursor, int64, error) {
	invalid := safeError(connect.CodeInvalidArgument, "invalid operation history page", panelv1.ErrorReason_ERROR_REASON_INVALID_INPUT)
	position := historyCursor{CreatedAt: math.MaxInt64}
	for _, filter := range []struct {
		input  *string
		target *string
	}{{request.EnvironmentId, &position.Environment}, {request.ServerId, &position.Server}} {
		if filter.input == nil {
			continue
		}
		id, err := uuid.Parse(*filter.input)
		if err != nil {
			return historyCursor{}, 0, invalid
		}
		*filter.target = id.String()
	}
	size := int64(request.GetPage().GetPageSize())
	if size == 0 {
		size = 50
	}
	if size < 1 || size > 100 || request.GetPage() == nil {
		return historyCursor{}, 0, invalid
	}
	token := request.GetPage().GetPageToken()
	if token != "" {
		if len(token) > 512 {
			return historyCursor{}, 0, invalid
		}
		encoded, err := base64.RawURLEncoding.DecodeString(token)
		var saved historyCursor
		if err != nil || json.Unmarshal(encoded, &saved) != nil || saved.Environment != position.Environment || saved.Server != position.Server || saved.CreatedAt < 0 {
			return historyCursor{}, 0, invalid
		}
		id, err := uuid.Parse(saved.ID)
		if err != nil || id.String() != saved.ID {
			return historyCursor{}, 0, invalid
		}
		position = saved
	}
	return position, size, nil
}

func historyOperation(value store.OperationHistory) *panelv1.Operation {
	if value.IsWaiting != 0 {
		return waitingOperation(store.WaitingBuild{ID: value.ID, State: value.State, Reason: value.Reason, ServerID: value.ServerID, EnvironmentID: value.EnvironmentID, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt})
	}
	return operationMessage(store.Operation{ID: value.ID, Kind: value.Kind, State: value.State, Reason: value.Reason, Outcome: value.Outcome, ServerID: value.ServerID, EnvironmentID: value.EnvironmentID, ReleaseID: value.ReleaseID, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt})
}
