package calendar

import "context"

// Service is the phase-one calendar application seam. It is deliberately
// provider-agnostic; a later provider/sync phase can depend on this interface
// without changing card persistence.
type Service interface {
	Create(ctx context.Context, event CalendarEvent) (*CalendarEvent, error)
	Get(ctx context.Context, id string) (*CalendarEvent, error)
	Update(ctx context.Context, id string, expectedRevision int64, event CalendarEvent) (*CalendarEvent, error)
	Cancel(ctx context.Context, id string, expectedRevision int64) (*CalendarEvent, error)
	Delete(ctx context.Context, id string, expectedRevision int64) (*CalendarEvent, error)
	List(ctx context.Context, query RangeQuery) ([]CalendarEvent, error)
}

// CalendarService delegates domain operations to a CalendarStore. Keeping this
// seam separate from CardRepository leaves HTTP/provider adapters for later
// phases and makes the current SQLite-backed path real in service tests.
type CalendarService struct {
	store CalendarStore
}

var _ Service = (*CalendarService)(nil)

// NewService constructs the phase-one service over store.
func NewService(store CalendarStore) *CalendarService {
	return &CalendarService{store: store}
}

func (s *CalendarService) Create(ctx context.Context, event CalendarEvent) (*CalendarEvent, error) {
	return s.store.Create(ctx, event)
}

func (s *CalendarService) Get(ctx context.Context, id string) (*CalendarEvent, error) {
	return s.store.Get(ctx, id)
}

func (s *CalendarService) Update(ctx context.Context, id string, expectedRevision int64, event CalendarEvent) (*CalendarEvent, error) {
	return s.store.Update(ctx, id, expectedRevision, event)
}

func (s *CalendarService) Cancel(ctx context.Context, id string, expectedRevision int64) (*CalendarEvent, error) {
	return s.store.Cancel(ctx, id, expectedRevision)
}

func (s *CalendarService) Delete(ctx context.Context, id string, expectedRevision int64) (*CalendarEvent, error) {
	return s.store.Delete(ctx, id, expectedRevision)
}

func (s *CalendarService) List(ctx context.Context, query RangeQuery) ([]CalendarEvent, error) {
	return s.store.List(ctx, query)
}
