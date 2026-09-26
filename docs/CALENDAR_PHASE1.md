# Calendar integration: phase 1

Phase 1 adds the provider-agnostic `internal/calendar` event domain, validation, service seam, and SQLite-backed store. Events are stored as `expanded` cards through the existing `CardDBManager`/`CardRepository` seam; no second calendar database is created. The stable card owner is `hermes.canopy.calendar`, and card data uses:

```json
{"schema":"hermes.canopy.calendar.event.v1","event":{...}}
```

The store supports create, get, optimistic-revision update, soft cancel/delete, and deterministic half-open range queries. Cancelled cards remain available for history and are excluded from ordinary range results.

## Composition root

`cmd/canopyd/main.go` constructs the existing `card.CardDBManager`, opens `calendar.NewCardCalendarStore(cardDBMgr)`, wraps it with `calendar.NewService`, and passes that service through `server.New`. The server retains the typed phase-one service in its dependency graph; HTTP routes and provider integrations remain deferred.

This phase has no provider integration and makes no calendar-provider claim. OAuth, CalDAV/ICS synchronization, auto-responder, SSE, HTTP handlers, and the frontend viewer are deferred to later phases. The existing plugin calendar permission/`NOT_IMPLEMENTED` behavior is unchanged.
