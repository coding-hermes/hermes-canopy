package handler

import (
	"net/http"

	"github.com/rs/zerolog/log"
)

// GET /api/v1/gateway/models (GAP-080 phase 2b).
//
// The model catalog the budget derivation is built on was invisible to the
// product: a user could not see which models the gateway knows, what context
// window each one reports, or what budget each would get — and the one UI
// surface that compiles a context (the manifest panel) could not name a model
// at all. This route is that catalog, read-only, for the UI to populate a
// model choice and to size its budget control.
//
// It adds NO new source of truth: the same *ModelWindowCatalog instance that
// serves GET /api/v1/context/{node_id} and POST /api/v1/gateway/runs is
// consulted here, through the same five-minute cache.
//
// Contract (mirrored in docs/API.md):
//
//	200 {"models":[{"id","context_window","desired_budget"}],
//	     "percent":int,"default_budget":int,"source":string}
//
// The route NEVER answers 5xx because the catalog is down. A catalog failure
// is an empty list plus source "catalog_error"; a server built without a
// catalog answers "no_catalog". `models` is `[]`, never `null`, in every
// failure mode — a null is a client-side crash waiting to happen, and the
// panel must degrade to "Server default" rather than to an error state.

// gatewayModelsResponse is the wire body of GET /api/v1/gateway/models.
//
// Models is deliberately not omitempty: the contract is an empty ARRAY on
// failure, and Models() is what guarantees the non-nil slice.
type gatewayModelsResponse struct {
	Models        []ModelWindow `json:"models"`
	Percent       int           `json:"percent"`
	DefaultBudget int           `json:"default_budget"`
	Source        string        `json:"source"`
}

// ListModels handles GET /api/v1/gateway/models.
//
// The catalog lookup is nil-safe on purpose: a GatewayHandler built without
// WithContextBudget carries a nil catalog, and Models reports "no_catalog"
// rather than panicking — the same fail-soft shape resolveDefaultBudget uses
// for the compile surfaces.
func (h *GatewayHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	models, source := h.windowCatalog.Models(r.Context(), h.budgetPercent, h.defaultBudget)
	log.Ctx(r.Context()).Debug().Str("source", source).Int("models", len(models)).
		Msg("gateway: listed model catalog")
	writeJSON(w, http.StatusOK, gatewayModelsResponse{
		Models:        models,
		Percent:       h.budgetPercent,
		DefaultBudget: h.defaultBudget,
		Source:        source,
	})
}
