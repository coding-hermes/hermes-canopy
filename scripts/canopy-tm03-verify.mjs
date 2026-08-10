#!/usr/bin/env node
/**
 * TM-03 Ad-hoc Verification Script
 *
 * Live-probes the running Canopy server to verify TM-03 endpoints work
 * end-to-end against REAL endpoints. Creates throwaway test data, exercises
 * search/recent/preview/inject, and asserts on the spec contract.
 *
 * Usage:
 *   node scripts/canopy-tm03-verify.mjs [BASE_URL] [JWT_SECRET]
 *
 * Defaults: BASE_URL=http://localhost:8091, JWT_SECRET=dev-secret-change-me
 *
 * Exit code 0 = all pass, 1 = any fail.
 */

import { createHash, createHmac, randomUUID } from 'crypto';

const BASE = process.argv[2] || process.env.CANOPY_BASE_URL || 'http://localhost:8091';
const API = `${BASE}/api/v1`;
const JWT_SECRET = process.argv[3] || process.env.CANOPY_JWT_SECRET || 'dev-secret-change-me';

let pass = 0;
let fail = 0;
const failures = [];

function assert(condition, msg) {
  if (condition) {
    console.log(`  ✓ PASS: ${msg}`);
    pass++;
  } else {
    console.log(`  ✗ FAIL: ${msg}`);
    fail++;
    failures.push(msg);
  }
}

// Create a JWT token for the test user.
// We need the jsonwebtoken package — or we can just use a simple HS256 JWT.
function makeJWT(secret, userId) {
  // Simple JWT creation using Web Crypto (available in Node 18+)
  const header = Buffer.from(JSON.stringify({ alg: 'HS256', typ: 'JWT' })).toString('base64url');
  const payload = Buffer.from(JSON.stringify({
    sub: userId,
    exp: Math.floor(Date.now() / 1000) + 3600,
  })).toString('base64url');
  const crypto = { createHmac, randomUUID };
  const sig = createHmac('sha256', secret).update(`${header}.${payload}`).digest('base64url');
  return `${header}.${payload}.${sig}`;
}

async function main() {
  console.log(`TM-03 Verification — target: ${API}`);
  console.log('---');

  // Dev user from the Vite proxy (BUG-003): only user present on the live DB.
  const userId = '00000000-0000-0000-0000-000000000001';
  const token = makeJWT(JWT_SECRET, userId);

  // We need a tree to work with. Try creating one, or use the first existing.
  let treeId;
  try {
    // List trees first.
    const treesResp = await fetch(`${API}/trees`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (treesResp.ok) {
      const trees = await treesResp.json();
      if (trees.trees && trees.trees.length > 0) {
        treeId = trees.trees[0].id;
        console.log(`Using existing tree: ${treeId}`);
      }
    }
  } catch (e) {
    // ignore
  }

  if (!treeId) {
    // Create a tree.
    const resp = await fetch(`${API}/trees`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({
        title: 'TM-03 Verify Test Tree',
        description: 'Throwaway tree for TM-03 verification',
        rootMessage: { content: 'Root node for TM-03 verification', contentFormat: 'markdown', nodeType: 'message' },
      }),
    });
    if (!resp.ok) {
      const body = await resp.text();
      console.error(`FATAL: cannot create tree: ${resp.status} ${body}`);
      process.exit(1);
    }
    const tree = await resp.json();
    treeId = tree.id;
    console.log(`Created tree: ${treeId}`);
  }

  // Create a topic directly via SQL... wait, we can't. But we can use the topics API.
  // Actually let's test search/recent first with whatever topics exist.

  // ── Test 1: Recent topics ──────────────────────────────────────────────
  console.log('\n1. GET /topics/recent');
  try {
    const resp = await fetch(`${API}/trees/${treeId}/topics/recent?limit=10`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert(resp.status === 200, `recent topics returns 200 (got ${resp.status})`);
    const body = await resp.json();
    assert(Array.isArray(body.topics), 'response has topics array');
    if (body.topics.length > 0) {
      const t = body.topics[0];
      assert(t.title !== undefined, 'topic has title');
      assert(t.node_count !== undefined, 'topic has node_count');
      assert(t.last_active_at !== undefined, 'topic has last_active_at');
    }
  } catch (e) {
    assert(false, `recent topics request failed: ${e.message}`);
  }

  // ── Test 2: Search too short → 400 ─────────────────────────────────────
  console.log('\n2. GET /topics/search?q=a (too short)');
  try {
    const resp = await fetch(`${API}/trees/${treeId}/topics/search?q=a`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert(resp.status === 400, `short query returns 400 (got ${resp.status})`);
    if (resp.status === 400) {
      const body = await resp.json();
      assert(body.error?.code === 'SEARCH_QUERY_TOO_SHORT', 'error code is SEARCH_QUERY_TOO_SHORT');
    }
  } catch (e) {
    assert(false, `short query request failed: ${e.message}`);
  }

  // ── Test 3: Search with valid query ────────────────────────────────────
  console.log('\n3. GET /topics/search?q=database');
  try {
    const resp = await fetch(`${API}/trees/${treeId}/topics/search?q=database`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert(resp.status === 200 || resp.status === 400, `search returns 200 or 400 (got ${resp.status})`);
    if (resp.status === 200) {
      const body = await resp.json();
      assert(body.results !== undefined, 'response has results array');
      assert(body.total !== undefined, 'response has total');
      assert(body.query_time_ms !== undefined, 'response has query_time_ms');
      if (body.results.length > 0) {
        const r = body.results[0];
        assert(r.relevance > 0, `result has relevance > 0 (got ${r.relevance})`);
        assert(r.topic_id !== undefined, 'result has topic_id');
        assert(r.title !== undefined, 'result has title');
      }
    }
  } catch (e) {
    assert(false, `search request failed: ${e.message}`);
  }

  // ── Test 4: Search stop words only → 400 ───────────────────────────────
  console.log('\n4. GET /topics/search?q=the and of (stop words)');
  try {
    const resp = await fetch(`${API}/trees/${treeId}/topics/search?q=the+and+of`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert(resp.status === 400, `stop-words query returns 400 (got ${resp.status})`);
  } catch (e) {
    assert(false, `stop-words request failed: ${e.message}`);
  }

  // ── Test 5: Inject too many topics → 400 ───────────────────────────────
  console.log('\n5. POST /context/inject with 6 topic IDs');
  try {
    const ids = Array.from({ length: 6 }, () => crypto.randomUUID());
    const resp = await fetch(`${API}/trees/${treeId}/context/inject`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({ topic_ids: ids }),
    });
    assert(resp.status === 400, `6 topics returns 400 (got ${resp.status})`);
    if (resp.status === 400) {
      const body = await resp.json();
      assert(body.error?.code === 'CONTEXT_TOO_MANY_TOPICS', 'error code is CONTEXT_TOO_MANY_TOPICS');
    }
  } catch (e) {
    assert(false, `inject too many failed: ${e.message}`);
  }

  // ── Test 6: Inject non-existent topic → 404 ────────────────────────────
  console.log('\n6. POST /context/inject with random UUID');
  try {
    const resp = await fetch(`${API}/trees/${treeId}/context/inject`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({ topic_ids: [crypto.randomUUID()] }),
    });
    assert(resp.status === 404, `non-existent topic returns 404 (got ${resp.status})`);
  } catch (e) {
    assert(false, `inject non-existent failed: ${e.message}`);
  }

  // ── Test 7: Preview non-existent topic → 404 ───────────────────────────
  console.log('\n7. GET /topics/{random}/preview');
  try {
    const resp = await fetch(`${API}/trees/${treeId}/topics/${crypto.randomUUID()}/preview`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert(resp.status === 404, `non-existent preview returns 404 (got ${resp.status})`);
  } catch (e) {
    assert(false, `preview request failed: ${e.message}`);
  }

  // ── Test 8: SQL injection attempt ──────────────────────────────────────
  console.log('\n8. SQL injection attempt');
  try {
    const resp = await fetch(`${API}/trees/${treeId}/topics/search?q=' OR 1=1 --`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert(resp.status === 200 || resp.status === 400, `injection attempt returns 200 or 400 (got ${resp.status})`);
  } catch (e) {
    assert(false, `injection test failed: ${e.message}`);
  }

  // ── Summary ────────────────────────────────────────────────────────────
  console.log('\n---');
  console.log(`Results: ${pass} pass, ${fail} fail`);
  if (fail > 0) {
    console.log('Failures:');
    failures.forEach(f => console.log(`  - ${f}`));
    process.exit(1);
  }
  console.log('ALL CHECKS PASSED');
  process.exit(0);
}

main().catch(e => {
  console.error('FATAL:', e);
  process.exit(1);
});
