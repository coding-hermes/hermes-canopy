2026-09-16 | PROMISING-BUT-ROUGH | 13s t2fs | friction 15 | 5 findings

2026-09-20 | PROMISING-BUT-ROUGH | collab/mls angle (2 users) | install_seconds=153 | bunker=las-bunker-03 agent=33c0cbb7 | smoke=ok | findings: DF-35..41 | t2fs ~18min wall (provisioning wall), 90s API once seeded
2026-09-21 | PROMISING-BUT-ROUGH | human-path core loop round 2 | install_to_health=3.707s, resume_first_page=510ms, nodes=4, viewers=7, gateway_events=11 | smoke=ok | findings: DF-42..44
2026-09-22 | PROMISING-BUT-ROUGH | MCP surface + production deploy path + source install | ttfb ~25min, cold_restart=0.160s, mcp_read=12.7ms, mcp_write=19.8ms, rest_read=12.3ms | bunker=las-bunker-03 agent=cc2d1bc3 (destroyed) | smoke=ok (shallow clone 408s + build 152s + pg 2s + canopyd 3s; CLI tree create/list on fresh box) | findings: DF-45..48 (SDK rejects all tools/call — missing content array; proxy --token+Basic never injects; node metadata base64 vs docs; MCP skips membership precheck)
