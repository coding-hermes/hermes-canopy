import { useState, useEffect } from 'react'
import { NavLink, Outlet, Routes, Route, useNavigate, useLocation } from 'react-router-dom'
import {
  LayoutDashboard,
  Trees,
  Network,
  Hash,
  LayoutGrid,
  ShieldCheck,
  GitBranch,
  MessageSquare,
  Bot,
  GitPullRequest,
  X,
} from 'lucide-react'
import TreeView from './components/TreeView'
import ApprovalPanel from './components/ApprovalPanel'
import TreesPage from './pages/TreesPage'
import NodesPage from './pages/NodesPage'
import TopicsPage from './pages/TopicsPage'
import CardsPage from './pages/CardsPage'
import WorkspacePage from './pages/WorkspacePage'
import AgentsPage from './pages/AgentsPage'
import ReviewPage from './pages/ReviewPage'
import { OfflineIndicator } from './components/OfflineIndicator'
import TopicsRail from './components/TopicsRail'
import TopicSearchPanel from './components/TopicSearchPanel'
import AppHeader from './components/AppHeader'
import ShortcutHelp from './components/ShortcutHelp'
import { useShortcuts } from './hooks/useShortcuts'
import { MERGE_ROUTE } from './lib/shortcuts'

// ─── Navigation model ──────────────────────────────────────────────────

interface NavItem {
  to: string
  label: string
  ariaLabel: string
  icon: typeof LayoutDashboard
  end?: boolean
}

const NAV_ITEMS: NavItem[] = [
  { to: '/', label: 'Dashboard', ariaLabel: 'Dashboard', icon: LayoutDashboard, end: true },
  { to: '/trees', label: 'Trees', ariaLabel: 'Trees', icon: Trees },
  { to: '/tree/demo', label: 'Tree View', ariaLabel: 'Tree View — demo tree', icon: GitBranch },
  { to: '/nodes', label: 'Nodes', ariaLabel: 'Nodes', icon: Network },
  { to: '/topics', label: 'Topics', ariaLabel: 'Topics', icon: Hash },
  { to: '/cards', label: 'Cards', ariaLabel: 'Cards', icon: LayoutGrid },
  { to: '/workspace', label: 'Workspace', ariaLabel: 'Workspace', icon: MessageSquare },
  { to: '/agents', label: 'Agents', ariaLabel: 'Agent roster', icon: Bot },
  { to: '/reviews', label: 'Reviews', ariaLabel: 'PR reviews', icon: GitPullRequest },
  { to: '/approvals', label: 'Approvals', ariaLabel: 'Approvals', icon: ShieldCheck },
]

const navLinkClass = (isActive: boolean) =>
  [
    'group flex items-center gap-2.5 px-3 py-2 rounded-md text-sm font-medium transition-colors',
    isActive
      ? 'bg-accent-2/12 text-accent-2-300 ring-1 ring-inset ring-accent-2/30'
      : 'text-content-muted hover:bg-surface-hover/60 hover:text-content-primary',
  ].join(' ')

function Dashboard() {
  return (
    <div className="p-6">
      <h1 className="text-3xl font-bold tracking-tight text-content-primary">
        Dashboard
      </h1>
      <p className="mt-2 text-content-muted">
        Welcome to Hermes Canopy — your knowledge canopy.
      </p>
    </div>
  )
}

function Layout() {
  const navigate = useNavigate()
  const location = useLocation()
  const [searchOpen, setSearchOpen] = useState(false)
  const [sidebarOpen, setSidebarOpen] = useState(false)

  // Mobile drawer: close on navigation so every route change lands on the
  // page, never on an open overlay (Bane 08-18: sidebar close in all modes).
  useEffect(() => {
    setSidebarOpen(false)
  }, [location.pathname])

  // Mobile drawer: ESC closes it (matches the help overlay convention).
  useEffect(() => {
    if (!sidebarOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setSidebarOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [sidebarOpen])

  // Ctrl/Cmd+K toggles the topic search panel (TM-03). Kept keyboard-only
  // so the default layout stays pixel-identical for the UI-09 goldens.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setSearchOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  /*
   * App-shell shortcut scope (UI-07): `m` jumps to the merge view and `?`
   * owns the help overlay. Tree-scoped keys (j/k/h/l) are registered by
   * TreeCanvas instead, so they are inert on pages without a graph.
   */
  const { helpOpen, setHelpOpen } = useShortcuts(
    { openMerge: () => navigate(MERGE_ROUTE) },
    { handleHelpToggle: true },
  )

  return (
    <div className="flex h-screen bg-surface-base text-content-secondary">
      {/* Offline indicator bar */}
      <OfflineIndicator />

      {/* Skip to main content link */}
      <a href="#main-content" className="skip-to-main">
        Skip to main content
      </a>

      {/* Mobile drawer backdrop — click to close (Bane 08-18) */}
      {sidebarOpen && (
        <div
          className="fixed inset-0 z-40 bg-black/50 md:hidden"
          onClick={() => setSidebarOpen(false)}
          aria-hidden="true"
          data-testid="sidebar-backdrop"
        />
      )}

      {/* Sidebar — single rail: nav buttons, divider, topics section (ChatGPT-style).
          Desktop: static flex child. Mobile: slide-in drawer (hidden by default,
          hamburger in AppHeader opens it, X / backdrop / ESC / route-change close it). */}
      <aside
        className={`w-72 shrink-0 bg-surface-panel border-r border-line-subtle flex flex-col
          fixed inset-y-0 left-0 z-50 transform transition-transform duration-200
          ${sidebarOpen ? 'translate-x-0' : '-translate-x-full'}
          md:static md:z-auto md:translate-x-0`}
        role="navigation"
        aria-label="Main navigation"
        data-testid="sidebar"
      >
        <div className="p-4 border-b border-line-subtle flex items-center justify-between">
          <span className="flex items-center gap-2 text-lg font-semibold tracking-tight text-content-primary">
            <span
              aria-hidden="true"
              className="grid h-7 w-7 place-items-center rounded-md bg-accent/15 text-accent ring-1 ring-inset ring-accent/30"
            >
              <Trees className="h-4 w-4" />
            </span>
            Canopy
          </span>
          {/* Mobile-only close button (Bane 08-18: close must exist in every UI mode) */}
          <button
            type="button"
            onClick={() => setSidebarOpen(false)}
            className="md:hidden p-1.5 rounded-md text-content-muted hover:bg-surface-hover hover:text-content-primary transition-colors"
            aria-label="Close navigation"
            data-testid="sidebar-close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>
        <nav className="p-3 space-y-1" aria-label="Primary navigation">
          {NAV_ITEMS.map(({ to, label, ariaLabel, icon: Icon, end }) => (
            <NavLink
              key={to}
              to={to}
              end={end}
              className={({ isActive }) => navLinkClass(isActive)}
              aria-label={ariaLabel}
            >
              <Icon className="h-4 w-4 shrink-0" aria-hidden="true" />
              {label}
            </NavLink>
          ))}
        </nav>

        {/* Horizontal rule then topics section — single sidebar, no second column */}
        <TopicsRail />

        <div className="p-4 border-t border-line-subtle">
          <p className="text-xs text-content-faint">Hermes Canopy v0.1.0</p>
        </div>
      </aside>

      {/* Topic search panel (TM-03) — 360px right panel, toggled from the sidebar header */}
      {searchOpen && (
        <div className="w-[360px] shrink-0 border-l border-line-subtle bg-surface-panel flex flex-col">
          <TopicSearchPanel />
        </div>
      )}

      {/* Main content area */}
      <div className="flex-1 flex flex-col overflow-hidden">
        {/* Header — context title, view selector, status (UI-03) */}
        <AppHeader onMenuClick={() => setSidebarOpen(true)} />

        {/* Content */}
        <main id="main-content" className="flex-1 overflow-auto" role="main">
          {/* ARIA live region for dynamic content announcements */}
          <div
            id="aria-live-announcer"
            role="status"
            aria-live="polite"
            aria-atomic="true"
          />
          <Outlet />
        </main>
      </div>

      {/* Shortcut help overlay — toggled by `?` (UI-07) */}
      <ShortcutHelp open={helpOpen} onClose={() => setHelpOpen(false)} />
    </div>
  )
}

export default function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="trees" element={<TreesPage />} />
        <Route path="tree/:treeId" element={<TreeView />} />
        <Route path="nodes" element={<NodesPage />} />
        <Route path="topics" element={<TopicsPage />} />
        <Route path="cards" element={<CardsPage />} />
        <Route path="workspace" element={<WorkspacePage />} />
        <Route path="agents" element={<AgentsPage />} />
        <Route path="reviews" element={<ReviewPage />} />
        <Route path="approvals" element={<ApprovalPanel />} />
      </Route>
    </Routes>
  )
}
