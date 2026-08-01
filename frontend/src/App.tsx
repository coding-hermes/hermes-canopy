import { NavLink, Outlet, Routes, Route } from 'react-router-dom'
import {
  LayoutDashboard,
  Trees,
  Network,
  Hash,
  LayoutGrid,
  ShieldCheck,
  GitBranch,
} from 'lucide-react'
import TreeView from './components/TreeView'
import ApprovalPanel from './components/ApprovalPanel'
import TreesPage from './pages/TreesPage'
import NodesPage from './pages/NodesPage'
import TopicsPage from './pages/TopicsPage'
import CardsPage from './pages/CardsPage'
import { OfflineIndicator } from './components/OfflineIndicator'
import TopicsRail from './components/TopicsRail'

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
  return (
    <div className="flex h-screen bg-surface-base text-content-secondary">
      {/* Offline indicator bar */}
      <OfflineIndicator />

      {/* Skip to main content link */}
      <a href="#main-content" className="skip-to-main">
        Skip to main content
      </a>

      {/* Sidebar */}
      <aside
        className="w-64 shrink-0 bg-surface-panel border-r border-line-subtle flex flex-col"
        role="navigation"
        aria-label="Main navigation"
      >
        <div className="p-4 border-b border-line-subtle">
          <span className="flex items-center gap-2 text-lg font-semibold tracking-tight text-content-primary">
            <span
              aria-hidden="true"
              className="grid h-7 w-7 place-items-center rounded-md bg-accent/15 text-accent ring-1 ring-inset ring-accent/30"
            >
              <Trees className="h-4 w-4" />
            </span>
            Canopy
          </span>
        </div>
        <nav className="flex-1 p-4 space-y-1" aria-label="Primary navigation">
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
        <div className="p-4 border-t border-line-subtle">
          <p className="text-xs text-content-faint">Hermes Canopy v0.1.0</p>
        </div>
      </aside>

      {/* Topics rail — persistent across routes (UI-02, mockup-1.png) */}
      <TopicsRail />

      {/* Main content area */}
      <div className="flex-1 flex flex-col overflow-hidden">
        {/* Header */}
        <header
          className="h-14 shrink-0 bg-surface-panel/80 backdrop-blur-md border-b border-line-subtle flex items-center px-6"
          role="banner"
        >
          <h2 className="text-sm font-medium text-content-tertiary">
            Knowledge Canopy
          </h2>
          <div className="ml-auto flex items-center gap-3">
            <span className="inline-flex items-center gap-1.5 rounded-sm bg-surface-input px-2 py-1 text-xs text-content-muted ring-1 ring-inset ring-line-subtle">
              <span
                aria-hidden="true"
                className="h-1.5 w-1.5 rounded-full bg-status-success"
              />
              Backend: localhost:8080
            </span>
          </div>
        </header>

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
        <Route path="approvals" element={<ApprovalPanel />} />
      </Route>
    </Routes>
  )
}
