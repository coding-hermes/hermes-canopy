package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// MCPHandler serves the Model Context Protocol JSON-RPC endpoint.
// Agents POST to /mcp to invoke Canopy operations programmatically.
type MCPHandler struct {
	treeSvc     service.TreeService
	nodeSvc     service.NodeService
	topicSvc    service.TopicService
	cardSvc     service.CardService
	graphSvc    service.GraphService
	approvalSvc service.ApprovalService
}

// NewMCPHandler creates an MCP handler wired to the given services.
func NewMCPHandler(
	treeSvc service.TreeService,
	nodeSvc service.NodeService,
	topicSvc service.TopicService,
	cardSvc service.CardService,
	graphSvc service.GraphService,
	approvalSvc service.ApprovalService,
) *MCPHandler {
	return &MCPHandler{
		treeSvc:     treeSvc,
		nodeSvc:     nodeSvc,
		topicSvc:    topicSvc,
		cardSvc:     cardSvc,
		graphSvc:    graphSvc,
		approvalSvc: approvalSvc,
	}
}

// Routes returns a chi router mounted at /mcp.
func (h *MCPHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.handleJSONRPC)
	return r
}

// ── JSON-RPC 2.0 types ────────────────────────────────────────────

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id"`
}

type jsonrpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
	ID      any       `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── Tool definitions ───────────────────────────────────────────────

type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string             `json:"type"`
	Properties map[string]propDef `json:"properties,omitempty"`
	Required   []string           `json:"required,omitempty"`
}

type propDef struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

var tools = []toolDef{
	{
		Name:        "list_trees",
		Description: "List all accessible trees with metadata",
		InputSchema: inputSchema{Type: "object"},
	},
	{
		Name:        "get_tree",
		Description: "Get a single tree by ID with stats and members",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propDef{
				"tree_id": {Type: "string", Description: "UUID of the tree"},
			},
			Required: []string{"tree_id"},
		},
	},
	{
		Name:        "create_node",
        Description: "Create a new node in a tree",
        InputSchema: inputSchema{
        	Type: "object",
        	Properties: map[string]propDef{
        		"tree_id":   {Type: "string", Description: "UUID of the tree (also set via AuthorID context)"},
        		"content":   {Type: "string", Description: "Markdown content for the node"},
        		"parent_id": {Type: "string", Description: "Optional parent node UUID"},
        	},
        	Required: []string{"tree_id", "content"},
        },
	},
	{
		Name:        "list_topics",
		Description: "List topics in a tree with optional status filter",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propDef{
				"tree_id": {Type: "string", Description: "UUID of the tree"},
				"status":  {Type: "string", Description: "Optional status filter (active, archived)"},
			},
			Required: []string{"tree_id"},
		},
	},
	{
		Name:        "get_graph_stats",
		Description: "Get aggregate graph statistics for a tree (node count, edge count, depth)",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propDef{
				"tree_id": {Type: "string", Description: "UUID of the tree"},
			},
			Required: []string{"tree_id"},
		},
	},
	{
		Name:        "list_approvals",
		Description: "List pending approvals for an owner",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propDef{
				"owner_id": {Type: "string", Description: "UUID of the approval owner"},
			},
			Required: []string{"owner_id"},
		},
	},
	{
		Name:        "list_cards",
		Description: "List cards with optional tree/node filter",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propDef{
				"tree_id": {Type: "string", Description: "Optional tree UUID filter"},
			},
		},
	},
}

// ── JSON-RPC dispatch ──────────────────────────────────────────────

func (h *MCPHandler) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "Parse error: "+err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		writeRPCError(w, req.ID, -32600, `Invalid Request: jsonrpc must be "2.0"`)
		return
	}

	switch req.Method {
	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{"tools": tools})
	case "tools/call":
		h.handleToolsCall(w, r, req)
	default:
		writeRPCError(w, req.ID, -32601, "Method not found: "+req.Method)
	}
}

func (h *MCPHandler) handleToolsCall(w http.ResponseWriter, r *http.Request, req jsonrpcRequest) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		writeRPCError(w, req.ID, -32602, "Invalid params: "+err.Error())
		return
	}

	result, err := h.dispatchTool(r.Context(), call.Name, call.Arguments)
	if err != nil {
		writeRPCError(w, req.ID, -32000, err.Error())
		return
	}
	writeRPCResult(w, req.ID, result)
}

// ── Tool dispatch ──────────────────────────────────────────────────

func (h *MCPHandler) dispatchTool(ctx context.Context, name string, args json.RawMessage) (any, error) {
	switch name {
	case "list_trees":
		return h.toolListTrees(ctx)
	case "get_tree":
		return h.toolGetTree(ctx, args)
	case "create_node":
		return h.toolCreateNode(ctx, args)
	case "list_topics":
		return h.toolListTopics(ctx, args)
	case "get_graph_stats":
		return h.toolGetGraphStats(ctx, args)
	case "list_approvals":
		return h.toolListApprovals(ctx, args)
	case "list_cards":
		return h.toolListCards(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// ── Tool implementations ───────────────────────────────────────────

func (h *MCPHandler) toolListTrees(ctx context.Context) (any, error) {
	result, err := h.treeSvc.ListTrees(ctx, service.ListTreesParams{Limit: 50})
	if err != nil {
		return nil, fmt.Errorf("list trees: %w", err)
	}
	return map[string]any{"trees": result.Trees}, nil
}

func (h *MCPHandler) toolGetTree(ctx context.Context, args json.RawMessage) (any, error) {
	var params struct {
		TreeID string `json:"tree_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	treeID, err := uuid.Parse(params.TreeID)
	if err != nil {
		return nil, fmt.Errorf("invalid tree_id UUID: %w", err)
	}
	tree, err := h.treeSvc.GetTree(ctx, treeID, service.GetTreeOptions{})
	if err != nil {
		return nil, fmt.Errorf("get tree: %w", err)
	}
	return map[string]any{"tree": tree}, nil
}

func (h *MCPHandler) toolCreateNode(ctx context.Context, args json.RawMessage) (any, error) {
	var params struct {
		TreeID   string `json:"tree_id"`
		Content  string `json:"content"`
		ParentID string `json:"parent_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	treeID, err := uuid.Parse(params.TreeID)
	if err != nil {
		return nil, fmt.Errorf("invalid tree_id UUID: %w", err)
	}
	if params.Content == "" {
		return nil, fmt.Errorf("content is required")
	}
	var parentID uuid.UUID
	if params.ParentID != "" {
		parentID, err = uuid.Parse(params.ParentID)
		if err != nil {
			return nil, fmt.Errorf("invalid parent_id UUID: %w", err)
		}
	}
	// Extract AuthorID from context (set by auth middleware).
	authorID := UserIDFromContext(ctx)
	result, err := h.nodeSvc.Create(ctx, treeID, service.CreateNodeInput{
		Content:  params.Content,
		ParentID: parentID,
		AuthorID: authorID,
		TreeID:   treeID,
	})
	if err != nil {
		return nil, fmt.Errorf("create node: %w", err)
	}
	return map[string]any{"node": result}, nil
}

func (h *MCPHandler) toolListTopics(ctx context.Context, args json.RawMessage) (any, error) {
	var params struct {
		TreeID string `json:"tree_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	treeID, err := uuid.Parse(params.TreeID)
	if err != nil {
		return nil, fmt.Errorf("invalid tree_id UUID: %w", err)
	}
	topics, err := h.topicSvc.ListTopics(ctx, treeID, params.Status, 50, 0)
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	return map[string]any{"topics": topics}, nil
}

func (h *MCPHandler) toolGetGraphStats(ctx context.Context, args json.RawMessage) (any, error) {
	var params struct {
		TreeID string `json:"tree_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	treeID, err := uuid.Parse(params.TreeID)
	if err != nil {
		return nil, fmt.Errorf("invalid tree_id UUID: %w", err)
	}
	stats, err := h.graphSvc.GetGraphStats(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("graph stats: %w", err)
	}
	return map[string]any{"stats": stats}, nil
}

func (h *MCPHandler) toolListApprovals(ctx context.Context, args json.RawMessage) (any, error) {
	var params struct {
		OwnerID string `json:"owner_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	ownerID, err := uuid.Parse(params.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("invalid owner_id UUID: %w", err)
	}
	approvals, total, err := h.approvalSvc.GetPending(ctx, ownerID, nil, 50, 0)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	return map[string]any{"approvals": approvals, "total": total}, nil
}

func (h *MCPHandler) toolListCards(ctx context.Context, args json.RawMessage) (any, error) {
	var params struct {
		TreeID string `json:"tree_id"`
	}
	_ = json.Unmarshal(args, &params) // tree_id is optional
	var treeID *uuid.UUID
	if params.TreeID != "" {
		id, err := uuid.Parse(params.TreeID)
		if err != nil {
			return nil, fmt.Errorf("invalid tree_id UUID: %w", err)
		}
		treeID = &id
	}
	cards, err := h.cardSvc.ListCards(ctx, treeID, nil, nil, 50, 0)
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}
	return map[string]any{"cards": cards}, nil
}

// ── JSON-RPC helpers ───────────────────────────────────────────────

func writeRPCResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{JSONRPC: "2.0", Result: result, ID: id})
}

func writeRPCError(w http.ResponseWriter, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	if code == -32700 {
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{
		JSONRPC: "2.0",
		Error:   &rpcError{Code: code, Message: message},
		ID:      id,
	})
}
