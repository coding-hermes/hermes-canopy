# SPEC-023: Shared Workspace UI (Web/PWA)

> Provenance: ported from the Helix repository — `specs/shared-workspace-ui.md` at
> commit `4d6d71a7ad62779fe2b73c67413004c46ecdec79` ("feat: Buzz gap specs + board
> reset — 4 new specs, 5 new tasks, cooldown 900s", 2026-07-29 21:29:06 -0500,
> author totalwindupflightsystems). Content below is byte-identical to that
> revision; the file header's own Author/Date line ("Helix Foreman (DeepSeek V4
> Pro)", 2026-07-30) is preserved unchanged.

**Status:** Draft · **Author:** Helix Foreman (DeepSeek V4 Pro) · **Date:** 2026-07-30
**Gap:** Helix is CLI-only. Buzz provides a Slack-like UI where humans see agents working together in real time. Helix needs a shared workspace.

## 1. Problem

Humans review agent work via `helix review dashboard --pr 1` from a terminal. There is no way to see multiple agents working across multiple repos simultaneously. Buzz proved that the "room" metaphor — humans and agents in shared channels — is the right UX.

## 2. Solution

A React + TypeScript PWA (already in MVP scope, deferred) that provides:
1. **Workspace view** — channels with humans + agents, real-time message feed
2. **PR Review panel** — change management dashboard (risk, blast, trust) inline
3. **Agent roster** — list of provisioned agents with trust scores and capability fingerprints
4. **Activity feed** — agent actions (commit, push, PR, review, merge) as timeline

## 3. Architecture

```
Frontend: React + TypeScript + Vite → PWA with Service Worker
Backend:  Canopyd SSE (server→client) + HTTP POST (client→server)
State:    Yjs/IndexedDB (local replica) + PostgreSQL (authoritative)
Transport: SSE push (agent events, trust updates) + REST API
```

## 4. Views

| View | Content |
|------|---------|
| **Workspace** | Channel list + message feed. Agents appear as channel members with trust badges |
| **PR Review** | Blast radius visualizer, risk gauge, trust timeline, Chimera verdict inline |
| **Agent Roster** | Provisioned agents: name, tier, trust score, capability radar, incident count |
| **Activity** | Timeline: "stepfun-tester pushed to feat/hello-world (+2 files)", "Chimera review: PASS (3/3 models)" |

## 5. API Endpoints (canopyd)

```
GET  /api/workspace/channels              → list channels
GET  /api/workspace/channels/:id/feed     → SSE event stream
POST /api/workspace/channels/:id/message  → send message
GET  /api/agents                          → agent roster
GET  /api/agents/:id                      → agent detail + trust history
GET  /api/activity                        → activity feed
POST /api/review/:pr                      → trigger Chimera review
```

## 6. Component Tree

```
App
├── Sidebar
│   ├── ChannelList
│   ├── AgentRoster (mini)
│   └── TrustScoreBadge
├── MainPanel
│   ├── MessageFeed (SSE-backed, real-time)
│   ├── PRReviewCard
│   │   ├── RiskGauge
│   │   ├── BlastRadiusViz (D3 force graph)
│   │   └── ChimeraVerdict
│   └── ActivityTimeline
└── TopBar
    ├── SearchBar
    └── UserMenu
```

## 7. State Shape (Yjs)

```typescript
type WorkspaceState = {
  channels: Map<string, Channel>
  agents: Map<string, AgentState>
  messages: Map<string, Message[]>
  activity: TimelineEvent[]
}

type AgentState = {
  id: string
  name: string
  tier: 'provisional' | 'established' | 'veteran'
  trustScore: number
  capabilities: Record<string, {success: number, total: number}>
  incidents: number
  lastActive: Date
}
```

## 8. Real-Time Transport

SSE from canopyd for:
- Agent state changes (trust score update, tier promotion)
- PR events (opened, reviewed, merged)
- Chimera review completion
- New messages in channels

Client reconnects with Last-Event-ID for gap-free delivery.

## 9. Files

```
canopy/
  src/
    components/
      Workspace/
        ChannelList.tsx
        MessageFeed.tsx
        MessageComposer.tsx
      Review/
        PRReviewCard.tsx
        RiskGauge.tsx
        BlastRadiusViz.tsx
        ChimeraVerdict.tsx
      Agent/
        AgentRoster.tsx
        AgentDetail.tsx
        TrustTimeline.tsx
        CapabilityRadar.tsx
      Activity/
        ActivityTimeline.tsx
    hooks/
      useSSE.ts
      useAgents.ts
      useWorkspace.ts
    store/
      yjsStore.ts
      workspaceSlice.ts
    api/
      canopyd.ts

canopyd/
  api/
    workspace.go
    agents.go
    activity.go
    sse.go
  store/
    workspace_store.go
    agent_store.go
```

## 10. Build Order

1. Scaffold Vite + React + TypeScript PWA skeleton
2. Canopyd SSE endpoint for agent events
3. Workspace view: channel list + message feed
4. Agent roster: list + detail + trust timeline
5. PR review panel: risk gauge, blast radius viz, Chimera verdict
6. Activity timeline
7. PWA service worker for offline
