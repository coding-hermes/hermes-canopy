package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// nodeListPageStub implements service.NodeListPager so the pagination
// handler branch can be exercised without a database (the handler falls
// back to ListByTree only when the service does NOT implement the pager).
type nodeListPageStub struct {
	service.NodeService
	page *service.NodeListPage
	err  error
}

func (s *nodeListPageStub) ListByTree(ctx context.Context, treeID uuid.UUID) ([]service.NodeDetail, error) {
	return []service.NodeDetail{}, nil
}

func (s *nodeListPageStub) ListByTreePage(ctx context.Context, treeID uuid.UUID, cursor *uuid.UUID, limit int) (*service.NodeListPage, error) {
	return s.page, s.err
}

func newPagerRouter(t *testing.T, stub service.NodeService) http.Handler {
	h := NewNodeHandler(stub, nil)
	r := chi.NewRouter()
	r.Get("/trees/{tree_id}/nodes", h.handleListByTree)
	return r
}

func TestNodeListPaging_ExtendedEnvelope(t *testing.T) {
	id := uuid.New()
	next := uuid.New()
	stub := &nodeListPageStub{page: &service.NodeListPage{
		Nodes: []service.NodeDetail{}, Total: 201, Limit: 50,
		HasMore: true, NextCursor: &next,
	}}
	srv := httptest.NewServer(newPagerRouter(t, stub))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/trees/" + id.String() + "/nodes?limit=50")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, float64(201), body["total"])
	require.Equal(t, float64(50), body["limit"])
	require.Equal(t, true, body["has_more"])
	require.Equal(t, next.String(), body["next_cursor"])
	_, ok := body["nodes"]
	require.True(t, ok)
}

func TestNodeListPaging_InvalidParams(t *testing.T) {
	stub := &nodeListPageStub{}
	srv := httptest.NewServer(newPagerRouter(t, stub))
	defer srv.Close()
	id := uuid.New()
	resp, err := http.Get(srv.URL + "/trees/" + id.String() + "/nodes?limit=0")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "INVALID_LIMIT", body.Error.Code)

	resp2, err := http.Get(srv.URL + "/trees/" + id.String() + "/nodes?limit=50&cursor=not-a-uuid")
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp2.StatusCode)
	var body2 struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&body2))
	require.Equal(t, "INVALID_CURSOR", body2.Error.Code)
}

func TestNodeListPaging_LegacyEnvelopeWithoutParams(t *testing.T) {
	// A pager-capable service still answers the legacy {"nodes":[...]}
	// envelope when the request carries NO limit/cursor parameters.
	stub := &nodeListPageStub{page: &service.NodeListPage{Nodes: []service.NodeDetail{}}}
	srv := httptest.NewServer(newPagerRouter(t, stub))
	defer srv.Close()
	id := uuid.New()
	resp, err := http.Get(srv.URL + "/trees/" + id.String() + "/nodes")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	_, hasTotal := body["total"]
	require.False(t, hasTotal, "no-params response must stay legacy-shaped")
	_, ok := body["nodes"]
	require.True(t, ok)
}
