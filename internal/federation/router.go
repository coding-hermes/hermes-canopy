package federation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RouteType string

const (
	RouteLocal  RouteType = "local"
	RouteRemote RouteType = "remote"
)

var ErrRouteNotFound = errors.New("federation: profile route not found")

type Route struct {
	ID        uuid.UUID  `json:"id"`
	ProfileID uuid.UUID  `json:"profile_id"`
	TreeID    *uuid.UUID `json:"tree_id,omitempty"`
	PeerID    *uuid.UUID `json:"peer_id,omitempty"`
	RouteType RouteType  `json:"route_type"`
	Priority  int        `json:"priority"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type RouteUpdate struct {
	TreeID    **uuid.UUID `json:"-"`
	PeerID    **uuid.UUID `json:"-"`
	RouteType *RouteType  `json:"-"`
	Priority  *int        `json:"-"`
}

type ProfileRouter interface {
	Create(context.Context, *Route) (*Route, error)
	List(context.Context, *uuid.UUID, *uuid.UUID) ([]Route, error)
	Update(context.Context, uuid.UUID, RouteUpdate) (*Route, error)
	Delete(context.Context, uuid.UUID) error
	Resolve(context.Context, uuid.UUID, uuid.UUID) (*Route, error)
}

type PGProfileRouter struct{ pool *pgxpool.Pool }

func NewPGProfileRouter(pool *pgxpool.Pool) *PGProfileRouter { return &PGProfileRouter{pool: pool} }

const routeColumns = `id, profile_id, tree_id, peer_id, route_type, priority, created_at, updated_at`

func scanRoute(row pgx.Row) (*Route, error) {
	var route Route
	if err := row.Scan(&route.ID, &route.ProfileID, &route.TreeID, &route.PeerID, &route.RouteType, &route.Priority, &route.CreatedAt, &route.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRouteNotFound
		}
		return nil, err
	}
	return &route, nil
}

func validateRoute(routeType RouteType, peerID *uuid.UUID) error {
	if routeType != RouteLocal && routeType != RouteRemote {
		return ErrInvalidInput
	}
	if routeType == RouteRemote && (peerID == nil || *peerID == uuid.Nil) {
		return ErrInvalidInput
	}
	if routeType == RouteLocal && peerID != nil {
		return ErrInvalidInput
	}
	return nil
}

func (r *PGProfileRouter) Create(ctx context.Context, route *Route) (*Route, error) {
	if route == nil || route.ProfileID == uuid.Nil || validateRoute(route.RouteType, route.PeerID) != nil {
		return nil, ErrInvalidInput
	}
	return scanRoute(r.pool.QueryRow(ctx, `INSERT INTO profile_routes (profile_id, tree_id, peer_id, route_type, priority)
		VALUES ($1,$2,$3,$4,$5) RETURNING `+routeColumns,
		route.ProfileID, route.TreeID, route.PeerID, route.RouteType, route.Priority))
}

func (r *PGProfileRouter) List(ctx context.Context, profileID, treeID *uuid.UUID) ([]Route, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+routeColumns+` FROM profile_routes
		WHERE ($1::uuid IS NULL OR profile_id=$1) AND ($2::uuid IS NULL OR tree_id=$2)
		ORDER BY priority DESC, created_at ASC, id ASC`, profileID, treeID)
	if err != nil {
		return nil, fmt.Errorf("federation: list routes: %w", err)
	}
	defer rows.Close()
	routes := make([]Route, 0)
	for rows.Next() {
		route, scanErr := scanRoute(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("federation: scan route: %w", scanErr)
		}
		routes = append(routes, *route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("federation: iterate routes: %w", err)
	}
	return routes, nil
}

func (r *PGProfileRouter) Update(ctx context.Context, id uuid.UUID, update RouteUpdate) (*Route, error) {
	if id == uuid.Nil {
		return nil, ErrInvalidInput
	}
	current, err := scanRoute(r.pool.QueryRow(ctx, `SELECT `+routeColumns+` FROM profile_routes WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	treeID, peerID, routeType, priority := current.TreeID, current.PeerID, current.RouteType, current.Priority
	if update.TreeID != nil {
		treeID = *update.TreeID
	}
	if update.PeerID != nil {
		peerID = *update.PeerID
	}
	if update.RouteType != nil {
		routeType = *update.RouteType
	}
	if update.Priority != nil {
		priority = *update.Priority
	}
	if err := validateRoute(routeType, peerID); err != nil {
		return nil, err
	}
	return scanRoute(r.pool.QueryRow(ctx, `UPDATE profile_routes SET tree_id=$2, peer_id=$3, route_type=$4, priority=$5, updated_at=now()
		WHERE id=$1 RETURNING `+routeColumns, id, treeID, peerID, routeType, priority))
}

func (r *PGProfileRouter) Delete(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return ErrInvalidInput
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM profile_routes WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRouteNotFound
	}
	return nil
}

func (r *PGProfileRouter) Resolve(ctx context.Context, profileID, treeID uuid.UUID) (*Route, error) {
	if profileID == uuid.Nil || treeID == uuid.Nil {
		return nil, ErrInvalidInput
	}
	return scanRoute(r.pool.QueryRow(ctx, `SELECT `+routeColumns+` FROM profile_routes
		WHERE profile_id=$1 AND (tree_id=$2 OR tree_id IS NULL)
		ORDER BY priority DESC, created_at ASC, id ASC LIMIT 1`, profileID, treeID))
}
