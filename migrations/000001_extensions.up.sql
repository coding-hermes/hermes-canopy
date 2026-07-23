-- 000001_extensions.up.sql
-- Install PostgreSQL extensions required by Canopy.
-- pgcrypto: gen_random_bytes / digest functions (fallback entropy).
-- pg_uuidv7: native UUIDv7 generation (preferred); if unavailable, the
-- fallback uuidv7() function below is used.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

DO $$ BEGIN
    CREATE EXTENSION IF NOT EXISTS "pg_uuidv7";
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'pg_uuidv7 extension not available; using fallback uuidv7() function';
END $$;

-- Fallback UUIDv7 generator. Uses bigint arithmetic to splice the
-- 48-bit Unix-millisecond timestamp with a 12-bit random field and a
-- 62-bit random field, then sets the version and variant nibbles per
-- RFC 9562 §5.7. Defined unconditionally so that tables with
-- DEFAULT uuidv7() always resolve to a callable symbol, regardless of
-- whether the pg_uuidv7 extension took precedence.
CREATE OR REPLACE FUNCTION uuidv7() RETURNS uuid AS $$
DECLARE
    ts_ms  bigint;
    rand_a bigint;
    rand_b bigint;
    bytes  bytea;
BEGIN
    ts_ms := (extract(epoch FROM clock_timestamp()) * 1000)::bigint;
    rand_a := (random() * 4096)::bigint;                     -- 12 bits
    rand_b := (random() * 4611686018427387904)::bigint;       -- 62 bits

    bytes := substring(int8send(ts_ms), 3, 6)
          || substring(int8send((7::bigint << 12) | rand_a), 7, 2)
          || substring(int8send((2::bigint << 62) | rand_b), 3, 8);

    -- Set version (0x7 in high nibble of byte 6).
    bytes := set_byte(bytes, 6, (get_byte(bytes, 6) & 0x0f) | 0x70);
    -- Set variant (10 in high two bits of byte 8).
    bytes := set_byte(bytes, 8, (get_byte(bytes, 8) & 0x3f) | 0x80);

    RETURN encode(bytes, 'hex')::uuid;
END;
$$ LANGUAGE plpgsql VOLATILE;
