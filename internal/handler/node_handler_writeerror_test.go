package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// TestWriteServiceErrorMapping is a table-driven unit test for
// NodeHandler.writeServiceError error-to-HTTP mapping (DF-HERMES-CANOPY-56).
func TestWriteServiceErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "tree not found maps to 404 TREE_NOT_FOUND",
			err:        service.ErrTreeNotFound,
			wantStatus: 404,
			wantCode:   "TREE_NOT_FOUND",
		},
		{
			name:       "wrapped tree not found maps to 404 TREE_NOT_FOUND",
			err:        fmt.Errorf("%w: insert node", service.ErrTreeNotFound),
			wantStatus: 404,
			wantCode:   "TREE_NOT_FOUND",
		},
		{
			name:       "node not found unchanged at 404 NOT_FOUND",
			err:        service.ErrNodeNotFound,
			wantStatus: 404,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "parent not found unchanged at 404 NOT_FOUND",
			err:        service.ErrParentNotFound,
			wantStatus: 404,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "unknown error falls to 500 INTERNAL_ERROR",
			err:        errors.New("some unexpected failure"),
			wantStatus: 500,
			wantCode:   "INTERNAL_ERROR",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/api/v1/trees/x/nodes", nil)
			h := &NodeHandler{}
			h.writeServiceError(w, r, tt.err)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding response body: %v", err)
			}
			if body.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.wantCode)
			}
		})
	}
}
