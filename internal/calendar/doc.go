// Package calendar contains phase-one calendar event domain and persistence
// support for Hermes Canopy.
//
// Phase 1 stores provider-agnostic CalendarEvent values as expanded cards in
// the existing card.CardDBManager/CardRepository SQLite composition seam. The
// stable app id is "hermes.canopy.calendar" and the card data is an explicit
// {"schema":"hermes.canopy.calendar.event.v1","event":{...}} envelope.
//
// The Service API is intentionally small: Create, Get, Update (with an
// expected revision), Cancel, Delete (an alias for soft cancellation), and
// List over a half-open time range. ErrEventNotFound and ErrRevisionConflict
// are distinct sentinel errors. Delete/Cancel preserve the card and mark the
// event cancelled so card revision and append-only event history remain
// available.
//
// This is not provider integration. OAuth, CalDAV, ICS synchronization,
// auto-responder, SSE, HTTP handlers, and the frontend viewer are deliberately
// deferred to later phases. The existing plugin calendar placeholders remain
// placeholders and are not used by this package.
package calendar
