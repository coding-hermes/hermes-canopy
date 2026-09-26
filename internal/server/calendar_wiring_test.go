package server

import (
	"path/filepath"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/calendar"
	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// TestCalendarCompositionRootWiring proves the phase-one calendar store and
// service reach the constructed server through the same New seam used by
// canopyd. Routes and providers are intentionally not part of this proof.
func TestCalendarCompositionRootWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CANOPY_GATEWAY_STATE_FILE", filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl"))

	manager := card.NewCardDBManager(t.TempDir())
	t.Cleanup(func() { _ = manager.Close() })

	store, err := calendar.NewCardCalendarStore(manager)
	if err != nil {
		t.Fatalf("NewCardCalendarStore: %v", err)
	}
	calendarSvc := calendar.NewService(store)

	srv := New(
		nil,                                 // healthProbe
		"",                                  // addr
		"calendar-wiring-secret",            // jwtSecret
		nil,                                 // treeSvc
		nil,                                 // nodeSvc
		nil,                                 // exportSvc
		nil,                                 // sseHub
		nil,                                 // syncEngine
		nil,                                 // approvalSvc
		nil,                                 // transportAdapter
		transport.NewConnectionManager(nil), // connMgr
		nil,                                 // transport selector
		nil,                                 // configRepo
		nil,                                 // eventRepo
		nil,                                 // membersRepo
		nil,                                 // userRepo
		nil,                                 // profileRouter
		nil,                                 // mlsHandler
		nil,                                 // topicSvc
		nil,                                 // cardSvc
		calendarSvc,
		nil, // iterationSvc
		nil, // graphSvc
		nil, // mergeSvc
		nil, // collabSvc
		nil, // metrics
		nil, // context compiler
		nil, // pluginSvc
		nil, // fileViewerSvc
		nil, // topicSearchSvc
		nil, // referenceSvc
		nil, // federationSvc
		nil, // relayRegistry
		config.Default(),
		nil, // dbPool
	)

	if srv.CalendarService() != calendarSvc {
		t.Fatalf("CalendarService() = %p, want %p", srv.CalendarService(), calendarSvc)
	}
}
