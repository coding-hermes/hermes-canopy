-- SPEC-FTR-02 P3: deterministic local/remote profile routing.
CREATE TABLE profile_routes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id  UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    tree_id     UUID REFERENCES trees(id) ON DELETE CASCADE,
    peer_id     UUID REFERENCES federation_peers(id) ON DELETE CASCADE,
    route_type  TEXT NOT NULL CHECK (route_type IN ('local', 'remote')),
    priority    INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT profile_routes_profile_tree_key UNIQUE NULLS NOT DISTINCT (profile_id, tree_id),
    CONSTRAINT profile_routes_peer_type_check CHECK (
        (route_type = 'remote' AND peer_id IS NOT NULL) OR
        (route_type = 'local' AND peer_id IS NULL)
    )
);

CREATE INDEX idx_profile_routes_lookup ON profile_routes(profile_id, tree_id, priority DESC, created_at ASC);
CREATE INDEX idx_profile_routes_peer ON profile_routes(peer_id) WHERE peer_id IS NOT NULL;
