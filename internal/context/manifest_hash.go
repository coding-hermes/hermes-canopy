package context

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Stable manifest digest (GAP-080 phase 5a).
//
// The product promise is that every model call has a visible, auditable
// context manifest. A manifest that is only DISPLAYED is not yet auditable:
// nothing binds the manifest a user reads to the payload a run was actually
// given. This file adds the binding — a sha256 over the manifest's
// content-bearing fields — so two manifests can be compared:
//
//   - the panel's preview compile (BEFORE the run), and
//   - the run record's manifest (AFTER the run, read back from the registry),
//
// hash equally when they describe the same compiled payload, and the digest
// of a stored record is recomputable by anyone holding its JSON.
//
// The digest is NOT a hash of the prompt text: it is a hash of the manifest,
// which is the artifact the UI shows. Hashing the rendered Content would be a
// different (and unverifiable-from-the-record) claim.

// ManifestDigest returns the stable, lowercase 64-hex sha256 digest of a
// manifest's content-bearing fields.
//
// The recipe, and why each step is here:
//
//  1. copy the manifest value,
//  2. clear the VOLATILE fields — RequestID and CompiledAt. They differ on
//     every compile of the same content (a fresh uuid and a fresh timestamp),
//     so including them would make the digest useless for the comparison it
//     exists for,
//  3. clear ManifestHash itself. A digest that fed its own output back into
//     its input would be self-referential: the value could never be
//     recomputed from the record,
//  4. json.Marshal the copy and sha256 the bytes, hex-encoded.
//
// Everything else stays IN the digest: NodeID, TokenBudget, TokensUsed,
// Ancestry, References, Cards, OmittedCount, OmittedReason, TruncationMarkers,
// Warnings, PinnedCount and MultiReference.
//
// Determinism: `encoding/json` marshals a struct's fields in declaration order
// and slices element-wise, so the same value always produces the same bytes.
// The hashed form must therefore never grow a map — Go randomizes map
// iteration and a map anywhere under Manifest would make the digest
// non-deterministic across processes. (`Manifest` has no map field today and
// must not gain one.)
//
// A note on the error path: json.Marshal cannot fail for this type (every
// field is a string, number, bool, time, UUID or a slice of those — no
// channels, funcs or cycles), so the signature is the pure one the callers
// want. Should that ever stop being true, the empty string returned here is
// not a digest and every caller treats "" as "no digest" — an honest
// degradation rather than a plausible-looking fake.
func ManifestDigest(m Manifest) string {
	raw, err := manifestCanonicalJSON(m)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ManifestDigestFromJSON recomputes the digest of a manifest carried as JSON —
// the audit primitive. `RunRecord.Manifest` holds the compiler manifest
// verbatim, so this is what turns a stored run record into a comparable
// digest.
//
// After unmarshalling, CompiledAt and RequestID are real values again and
// ManifestHash may be present, so the JSON path must zero all three before
// hashing — exactly like the value path, which it delegates to. (A time.Time
// round-trips through RFC3339 and reaches the zero value only when something
// explicitly zeroes it; comparing or normalising timestamps instead would make
// the two paths disagree the moment a record predates a field.)
//
// A record written BEFORE this field existed decodes to the zero string for
// ManifestHash and yields a digest that matches a value manifest built without
// the field — an old manifest is still comparable to a new one describing the
// same content.
//
// An empty or malformed document is an error: there is no manifest to hash,
// and returning a digest for "nothing" would be the hollow-fact failure mode
// this audit surface exists to prevent.
func ManifestDigestFromJSON(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("context: empty manifest JSON")
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("context: decode manifest: %w", err)
	}
	return ManifestDigest(m), nil
}

// manifestCanonicalJSON marshals the hashed form of a manifest: the value with
// every volatile field cleared.
func manifestCanonicalJSON(m Manifest) ([]byte, error) {
	canonical := m
	canonical.RequestID = ""
	canonical.CompiledAt = time.Time{} // must be the ZERO time, not a truncated copy
	canonical.ManifestHash = ""
	return json.Marshal(canonical)
}
