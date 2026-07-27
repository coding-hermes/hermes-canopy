#!/usr/bin/env python3
"""
Hermes Canopy — Comprehensive Accessibility Audit
Uses Playwright + axe-core to audit all pages for WCAG 2.1 AA compliance.
"""

import json, os, sys, time
from pathlib import Path

BASE_URL = os.environ.get("BASE_URL", "http://localhost:5173")
AXE_PATH = "/home/kara/hermes-canopy/frontend/node_modules/axe-core/axe.min.js"
OUTPUT_DIR = "/home/kara/hermes-canopy/frontend/test-results"

PAGES = [
    ("/", "Dashboard", "App.tsx (Dashboard)"),
    ("/trees", "TreesPage", "pages/TreesPage.tsx"),
    ("/nodes", "NodesPage", "pages/NodesPage.tsx"),
    ("/topics", "TopicsPage", "pages/TopicsPage.tsx"),
    ("/cards", "CardsPage", "pages/CardsPage.tsx"),
    ("/approvals", "ApprovalPanel", "components/ApprovalPanel.tsx"),
    ("/tree/demo", "TreeView", "components/TreeView.tsx"),
]

def main():
    from playwright.sync_api import sync_playwright

    axe_source = Path(AXE_PATH).read_text()
    print("=== Hermes Canopy Accessibility Audit ===")
    print(f"Target: WCAG 2.1 AA | Base URL: {BASE_URL}")
    print(f"Axe-core loaded: {len(axe_source)} bytes\n")

    all_results = []

    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        context = browser.new_context(viewport={"width": 1280, "height": 720})
        page = context.new_page()

        for route, name, component in PAGES:
            print(f"\n{'='*60}")
            print(f"📋 {name} ({route}) — {component}")
            print(f"{'='*60}")

            page_result = {"route": route, "name": name, "component": component}

            # Navigate
            try:
                page.goto(f"{BASE_URL}{route}", wait_until="networkidle", timeout=15000)
                time.sleep(1.0)  # Let React hydrate
            except Exception as e:
                print(f"  ⚠️ Navigation failed: {e}")
                page_result["navigation_error"] = str(e)
                all_results.append(page_result)
                continue

            title = page.title()
            print(f"  Title: {title}")
            page_result["title"] = title

            # ── AXE-CORE ──────────────────────────────────────────
            print("  Running axe-core...")
            # Inject axe
            page.evaluate(axe_source)

            axe_results = page.evaluate("""
                () => {
                    return new Promise((resolve) => {
                        axe.run(document, {
                            runOnly: {
                                type: 'tag',
                                values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']
                            }
                        }, (err, results) => {
                            if (err) resolve({ error: err.message, violations: [], passes: [] });
                            else resolve(results);
                        });
                    });
                }
            """)

            violations = axe_results.get("violations", [])
            passes = axe_results.get("passes", [])
            axe_error = axe_results.get("error")

            page_result["axe_violations"] = violations
            page_result["axe_passes_count"] = len(passes)
            page_result["axe_error"] = axe_error

            print(f"  Axe violations: {len(violations)}")
            for v in violations:
                impact = v.get("impact", "unknown")
                icon = "🔴" if impact == "critical" else "🟠" if impact == "serious" else "🟡" if impact == "moderate" else "🔵"
                nodes = v.get("nodes", [])
                print(f"    {icon} [{impact}] {v['id']}: {v['help']} ({len(nodes)} instances)")
                for n in nodes[:3]:  # Show first 3 instances
                    targets = ", ".join(n.get("target", []))
                    print(f"       → {targets}")
                    if n.get("failureSummary"):
                        summary = n.get("failureSummary", "")[:120]
                        print(f"         {summary}")

            # ── KEYBOARD NAVIGATION ───────────────────────────────
            print("  ⌨️  Keyboard navigation test...")
            keyboard_results = []
            prev_id = None

            for i in range(40):
                page.keyboard.press("Tab")
                time.sleep(0.08)

                focus_info = page.evaluate("""
                    () => {
                        const el = document.activeElement;
                        if (!el || el === document.body) return null;
                        const cs = window.getComputedStyle(el);
                        const rect = el.getBoundingClientRect();
                        const hasOutline = cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0;
                        const hasBoxShadow = cs.boxShadow !== 'none' && cs.boxShadow !== 'rgba(0, 0, 0, 0) none';
                        return {
                            tag: el.tagName.toLowerCase(),
                            id: el.id || null,
                            ariaLabel: el.getAttribute('aria-label') || null,
                            role: el.getAttribute('role') || null,
                            text: (el.textContent || '').trim().slice(0, 50),
                            visible: rect.width > 0 && rect.height > 0,
                            hasFocusIndicator: hasOutline || hasBoxShadow || cs.outlineStyle !== 'none',
                            outlineStyle: cs.outlineStyle,
                            outlineWidth: cs.outlineWidth,
                            boxShadow: cs.boxShadow.slice(0, 60),
                        };
                    }
                """)

                if not focus_info:
                    print(f"    Tab #{i+1}: No focusable element")
                    break

                unique_id = focus_info.get("id") or focus_info.get("ariaLabel") or focus_info.get("text")

                if prev_id and prev_id == unique_id:
                    print(f"    Tab #{i+1}: Loop detected → same element as before")
                    break
                prev_id = unique_id

                has_indicator = focus_info.get("hasFocusIndicator", False)
                indicator_icon = "✅" if has_indicator else "❌"
                print(f"    Tab #{i+1}: {indicator_icon} {focus_info['tag']}{'#'+focus_info['id'] if focus_info.get('id') else ''} \"{focus_info['text']}\"")

                keyboard_results.append({
                    "tab_num": i + 1,
                    **focus_info,
                    "focus_indicator_missing": not has_indicator,
                })

            page_result["keyboard_tab_count"] = len(keyboard_results)
            page_result["keyboard_missing_indicators"] = sum(1 for k in keyboard_results if k["focus_indicator_missing"])
            page_result["keyboard_results"] = keyboard_results

            # ── SCREEN READER / STATIC CHECKS ─────────────────────
            print("  🔊 Screen reader / static checks...")
            sr_checks = page.evaluate("""
                () => {
                    const results = [];

                    // 1. ARIA live region
                    const live = document.getElementById('aria-live-announcer');
                    results.push({
                        check: 'ARIA live region present',
                        pass: !!live,
                        detail: live ? `Found (role=${live.getAttribute('role')}, aria-live=${live.getAttribute('aria-live')})` : 'Missing'
                    });

                    // 2. Skip-to-main link
                    const skip = document.querySelector('.skip-to-main');
                    results.push({
                        check: 'Skip-to-main link',
                        pass: !!skip,
                        detail: skip ? skip.textContent.trim() : 'Missing'
                    });

                    // 3. lang attribute
                    results.push({
                        check: 'HTML lang attribute',
                        pass: !!document.documentElement.lang,
                        detail: document.documentElement.lang || 'Missing'
                    });

                    // 4. Main role
                    const main = document.querySelector('main');
                    results.push({
                        check: '<main> has role="main"',
                        pass: main ? main.getAttribute('role') === 'main' : false,
                        detail: main ? `role=${main.getAttribute('role')}` : 'No <main>'
                    });

                    // 5. Sidebar navigation
                    const sidebar = document.querySelector('aside');
                    results.push({
                        check: 'Sidebar <aside> role="navigation"',
                        pass: sidebar ? sidebar.getAttribute('role') === 'navigation' : false,
                        detail: sidebar ? `role=${sidebar.getAttribute('role')}` : 'No <aside>'
                    });

                    // 6. Heading hierarchy
                    const headings = Array.from(document.querySelectorAll('h1,h2,h3,h4,h5,h6'));
                    const levels = headings.map(h => parseInt(h.tagName[1]));
                    const skipped = [];
                    for (let i = 1; i < levels.length; i++) {
                        if (levels[i] > levels[i-1] + 1) {
                            skipped.push(`h${levels[i-1]}→h${levels[i]}`);
                        }
                    }
                    results.push({
                        check: 'Heading hierarchy (no skips)',
                        pass: skipped.length === 0,
                        detail: skipped.length ? `Skipped: ${skipped.join(', ')}` : `${headings.length} headings: [${levels.join(',')}]`
                    });

                    // 7. Alt text
                    const imgs = Array.from(document.querySelectorAll('img'));
                    const missingAlt = imgs.filter(img => !img.hasAttribute('alt'));
                    results.push({
                        check: 'All images have alt text',
                        pass: missingAlt.length === 0,
                        detail: missingAlt.length ? `${missingAlt.length}/${imgs.length} missing` : `${imgs.length} all have alt`
                    });

                    // 8. Form labels
                    const inputs = Array.from(document.querySelectorAll('input:not([type="hidden"]), textarea, select'));
                    const unlabeled = [];
                    for (const inp of inputs) {
                        const id = inp.id;
                        const hasLabel = id ? !!document.querySelector(`label[for="${CSS.escape(id)}"]`) : false;
                        if (!hasLabel && !inp.hasAttribute('aria-label') && !inp.hasAttribute('aria-labelledby') && !inp.closest('label')) {
                            unlabeled.push(inp.placeholder || inp.name || inp.id || inp.className.slice(0,30));
                        }
                    }
                    results.push({
                        check: 'All inputs have labels',
                        pass: unlabeled.length === 0,
                        detail: unlabeled.length ? `${unlabeled.length}/${inputs.length} unlabeled` : `${inputs.length} all labeled`
                    });

                    // 9. h1 count (should be exactly 1)
                    const h1s = document.querySelectorAll('h1');
                    results.push({
                        check: 'Exactly one h1 per page',
                        pass: h1s.length === 1,
                        detail: `${h1s.length} h1(s): [${Array.from(h1s).map(h => h.textContent.trim().slice(0,30)).join(', ')}]`
                    });

                    // 10. ARIA roles on interactive elements
                    const interactiveWithoutRoles = Array.from(document.querySelectorAll(
                        'div[onclick], div[role="button"], span[onclick], span[role="button"]'
                    )).filter(el => !el.getAttribute('role') && !el.hasAttribute('tabindex'));
                    results.push({
                        check: 'Interactive divs have ARIA roles',
                        pass: interactiveWithoutRoles.length === 0,
                        detail: interactiveWithoutRoles.length ? `${interactiveWithoutRoles.length} missing roles` : 'All have roles'
                    });

                    return results;
                }
            """)

            for c in sr_checks:
                icon = "✅" if c["pass"] is True else "❌" if c["pass"] is False else "➖"
                print(f"    {icon} {c['check']}: {c['detail']}")

            page_result["screen_reader_checks"] = sr_checks
            page_result["sr_pass_count"] = sum(1 for c in sr_checks if c["pass"] is True)
            page_result["sr_fail_count"] = sum(1 for c in sr_checks if c["pass"] is False)
            page_result["sr_total"] = len(sr_checks)

            all_results.append(page_result)

        browser.close()

    # ── WRITE RAW JSON ─────────────────────────────────────────────
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    json_path = os.path.join(OUTPUT_DIR, "accessibility-audit-raw.json")
    with open(json_path, "w") as f:
        json.dump(all_results, f, indent=2, default=str)
    print(f"\n📄 Raw data: {json_path}")

    # ── SUMMARY ────────────────────────────────────────────────────
    total_axe = sum(len(r.get("axe_violations", [])) for r in all_results)
    critical = sum(1 for r in all_results for v in r.get("axe_violations", []) if v.get("impact") == "critical")
    serious = sum(1 for r in all_results for v in r.get("axe_violations", []) if v.get("impact") == "serious")
    moderate = sum(1 for r in all_results for v in r.get("axe_violations", []) if v.get("impact") == "moderate")
    total_kb_issues = sum(r.get("keyboard_missing_indicators", 0) for r in all_results)

    print("\n" + "="*60)
    print("SUMMARY")
    print("="*60)
    print(f"  Pages audited: {len(all_results)}")
    print(f"  Axe violations: {total_axe} total ({critical} critical, {serious} serious, {moderate} moderate)")
    print(f"  Keyboard focus issues: {total_kb_issues}")
    for r in all_results:
        sr = r.get("screen_reader_checks", [])
        passes = sum(1 for c in sr if c["pass"] is True)
        fails = sum(1 for c in sr if c["pass"] is False)
        vcount = len(r.get("axe_violations", []))
        kcount = r.get("keyboard_tab_count", 0)
        kmiss = r.get("keyboard_missing_indicators", 0)
        print(f"  {r['name']}: {vcount} axe violations, {kcount} tab stops ({kmiss} missing focus), SR {passes}/{len(sr)}")

    return all_results

if __name__ == "__main__":
    main()
