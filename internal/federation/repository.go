package federation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGRepository struct{ pool *pgxpool.Pool }

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// NewPersistentService loads or atomically creates the singleton Ed25519
// identity stored in federation_identity. ECDH session material is never stored.
func (r *PGRepository) NewPersistentService(ctx context.Context, legacyKey []byte, serverID uuid.UUID, serverURL string) (*Service, error) {
	var publicKey, privateKey []byte
	err := r.pool.QueryRow(ctx, `SELECT public_key, private_key FROM federation_identity WHERE singleton=true`).Scan(&publicKey, &privateKey)
	if errors.Is(err, pgx.ErrNoRows) {
		publicKey, privateKey, err = ed25519.GenerateKey(rand.Reader)
		if err == nil {
			err = r.pool.QueryRow(ctx, `INSERT INTO federation_identity (singleton, public_key, private_key) VALUES (true,$1,$2)
				ON CONFLICT (singleton) DO UPDATE SET singleton=EXCLUDED.singleton
				RETURNING public_key, private_key`, publicKey, privateKey).Scan(&publicKey, &privateKey)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("federation: load signing identity: %w", err)
	}
	if len(publicKey) != ed25519.PublicKeySize || len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("federation: invalid persisted signing identity")
	}
	return newService(r, newTokenSignerWithIdentity(legacyKey, ed25519.PrivateKey(privateKey), serverID, serverURL)), nil
}

const peerColumns = `id, server_url, signing_key_fp, ecdhe_public_key, signing_public_key, role, state,
tree_id, created_by, created_at, connected_at, last_heartbeat, revoked_at, revoke_reason`

func scanPeer(row pgx.Row) (*FederationPeer, error) {
	var p FederationPeer
	if err := row.Scan(&p.ID, &p.ServerURL, &p.SigningKeyFP, &p.ECDHEPublicKey, &p.SigningPublicKey, &p.Role, &p.State,
		&p.TreeID, &p.CreatedBy, &p.CreatedAt, &p.ConnectedAt, &p.LastHeartbeat, &p.RevokedAt, &p.RevokeReason); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFederationNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *PGRepository) Create(ctx context.Context, p *FederationPeer) (*FederationPeer, error) {
	return scanPeer(r.pool.QueryRow(ctx, `INSERT INTO federation_peers
        (server_url, signing_key_fp, ecdhe_public_key, signing_public_key, role, state, tree_id, created_by, connected_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+peerColumns,
		p.ServerURL, p.SigningKeyFP, p.ECDHEPublicKey, p.SigningPublicKey, p.Role, p.State, p.TreeID, p.CreatedBy, p.ConnectedAt))
}

func (r *PGRepository) UpsertAccepted(ctx context.Context, p *FederationPeer) (*FederationPeer, error) {
	return scanPeer(r.pool.QueryRow(ctx, `INSERT INTO federation_peers
        (server_url, signing_key_fp, ecdhe_public_key, signing_public_key, role, state, tree_id, created_by, connected_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
        ON CONFLICT (server_url, tree_id) DO UPDATE SET
          signing_key_fp=EXCLUDED.signing_key_fp, ecdhe_public_key=EXCLUDED.ecdhe_public_key, signing_public_key=EXCLUDED.signing_public_key,
          state=EXCLUDED.state, connected_at=EXCLUDED.connected_at
        WHERE federation_peers.state <> $10
        RETURNING `+peerColumns,
		p.ServerURL, p.SigningKeyFP, p.ECDHEPublicKey, p.SigningPublicKey, p.Role, p.State, p.TreeID, p.CreatedBy, p.ConnectedAt, PeerRevoked))
}

func (r *PGRepository) Get(ctx context.Context, id uuid.UUID) (*FederationPeer, error) {
	return scanPeer(r.pool.QueryRow(ctx, `SELECT `+peerColumns+` FROM federation_peers WHERE id=$1`, id))
}

func (r *PGRepository) FindByServerTree(ctx context.Context, serverURL string, treeID uuid.UUID) (*FederationPeer, error) {
	return scanPeer(r.pool.QueryRow(ctx, `SELECT `+peerColumns+` FROM federation_peers WHERE server_url=$1 AND tree_id=$2`, serverURL, treeID))
}

func (r *PGRepository) List(ctx context.Context, treeID *uuid.UUID, activeOnly bool) ([]*FederationPeer, error) {
	query := `SELECT ` + peerColumns + ` FROM federation_peers WHERE ($1::uuid IS NULL OR tree_id=$1)`
	if activeOnly {
		query += ` AND state <> 4`
	}
	query += ` ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query, treeID)
	if err != nil {
		return nil, fmt.Errorf("query peers: %w", err)
	}
	defer rows.Close()
	out := make([]*FederationPeer, 0)
	for rows.Next() {
		peer, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan peer: %w", err)
		}
		out = append(out, peer)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peers: %w", err)
	}
	return out, nil
}

func (r *PGRepository) SetState(ctx context.Context, id uuid.UUID, state PeerState, revokedAt *time.Time, reason *string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE federation_peers SET state=$2, revoked_at=$3, revoke_reason=$4 WHERE id=$1`, id, state, revokedAt, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrFederationNotFound
	}
	return nil
}
