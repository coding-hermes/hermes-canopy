package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

type contractTreeService struct {
	service.TreeService
	got service.CreateTreeParams
}

func (s *contractTreeService) CreateTree(_ context.Context, params service.CreateTreeParams) (*service.Tree, error) {
	s.got = params
	return &service.Tree{ID: uuid.New(), Title: params.Title}, nil
}

type contractTopicService struct {
	service.TopicService
	created *service.TopicSummary
	err     error
}

func (s *contractTopicService) CreateTopic(_ context.Context, treeID, rootNodeID uuid.UUID, title, description string) (*service.TopicSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := s.created
	if out == nil {
		out = &service.TopicSummary{ID: uuid.New(), TreeID: treeID, RootNodeID: rootNodeID, Title: title, Description: description}
	}
	return out, nil
}

// TestDocumentedCreateContractsAccepted anchors the handler probes to the
// request shapes documented in docs/API.md. It intentionally omits every
// field labeled optional from the create requests.
func TestDocumentedCreateContractsAccepted(t *testing.T) {
	docs, err := os.ReadFile("../../docs/API.md")
	if err != nil {
		t.Fatalf("read docs/API.md: %v", err)
	}
	for _, documented := range []string{
		`"contentFormat": "string (optional`,
		`"nodeType": "string (optional`,
		`"description": "string (optional)"`,
	} {
		if !strings.Contains(string(docs), documented) {
			t.Fatalf("docs/API.md no longer contains contract marker %q", documented)
		}
	}

	t.Run("tree optional fields omitted", func(t *testing.T) {
		svc := &contractTreeService{}
		h := NewTreeHandler(svc, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/trees", strings.NewReader(`{
			"title":"Contract tree","rootMessage":{"content":"root"}
		}`))
		req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, uuid.New()))
		rr := httptest.NewRecorder()

		h.CreateTree(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
		}
		if svc.got.ContentFormat != service.FormatMarkdown || svc.got.NodeType != service.NodeTypeMessage {
			t.Fatalf("optional defaults = (%q, %q), want (markdown, message)", svc.got.ContentFormat, svc.got.NodeType)
		}
	})

	t.Run("topic optional description omitted", func(t *testing.T) {
		h := NewTopicHandler(&contractTopicService{})
		body := fmt.Sprintf(`{"treeId":%q,"rootNodeId":%q,"title":"Contract topic"}`, uuid.New(), uuid.New())
		rr := httptest.NewRecorder()
		h.CreateTopic(rr, httptest.NewRequest(http.MethodPost, "/api/v1/topics", strings.NewReader(body)))
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestCreateTopicReturnsGeneratedID(t *testing.T) {
	want := uuid.New()
	h := NewTopicHandler(&contractTopicService{created: &service.TopicSummary{ID: want, Title: "Real ID"}})
	rr := httptest.NewRecorder()
	body := fmt.Sprintf(`{"treeId":%q,"rootNodeId":%q,"title":"Real ID"}`, uuid.New(), uuid.New())
	h.CreateTopic(rr, httptest.NewRequest(http.MethodPost, "/api/v1/topics", strings.NewReader(body)))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var got service.TopicSummary
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != want || got.ID == uuid.Nil {
		t.Fatalf("response id = %s, want generated id %s", got.ID, want)
	}
}

func TestCreateTopicSoftDeletedTreeReturnsJSON404(t *testing.T) {
	h := NewTopicHandler(&contractTopicService{err: fmt.Errorf("service: tree not found: %w", service.ErrTopicTreeNotFound)})
	rr := httptest.NewRecorder()
	body := fmt.Sprintf(`{"treeId":%q,"rootNodeId":%q,"title":"Deleted tree"}`, uuid.New(), uuid.New())
	h.CreateTopic(rr, httptest.NewRequest(http.MethodPost, "/api/v1/topics", strings.NewReader(body)))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Error.Code != "TREE_NOT_FOUND" {
		t.Fatalf("error code = %q, want TREE_NOT_FOUND", envelope.Error.Code)
	}
}
