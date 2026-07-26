import { NavLink, Outlet, Routes, Route } from 'react-router-dom'
import TreeView from './components/TreeView'
import ApprovalPanel from './components/ApprovalPanel'
import TreesPage from './pages/TreesPage'
import NodesPage from './pages/NodesPage'
import TopicsPage from './pages/TopicsPage'
import CardsPage from './pages/CardsPage'
import { OfflineIndicator } from './components/OfflineIndicator'

function Dashboard() {
  return (
    <div className="p-6">
      <h1 className="text-3xl font-bold text-gray-900 dark:text-white">Dashboard</h1>
      <p className="mt-2 text-gray-600 dark:text-gray-400">
        Welcome to Hermes Canopy — your knowledge canopy.
      </p>
    </div>
  )
}

function Layout() {
  return (
    <div className="flex h-screen bg-gray-50 dark:bg-gray-950">
      {/* Offline indicator bar */}
      <OfflineIndicator />

      {/* Skip to main content link */}
      <a href="#main-content" className="skip-to-main">
        Skip to main content
      </a>

      {/* Sidebar */}
      <aside
        className="w-64 bg-white dark:bg-gray-900 border-r border-gray-200 dark:border-gray-800 flex flex-col"
        role="navigation"
        aria-label="Main navigation"
      >
        <div className="p-4 border-b border-gray-200 dark:border-gray-800">
          <span className="text-lg font-semibold text-gray-900 dark:text-white">
            🌳 Canopy
          </span>
        </div>
        <nav className="flex-1 p-4 space-y-1" aria-label="Primary navigation">
          <NavLink
            to="/"
            className={({ isActive }) =>
              `block px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                isActive
                  ? 'bg-purple-100 dark:bg-purple-900/30 text-purple-700 dark:text-purple-300'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 hover:text-gray-900 dark:hover:text-white'
              }`
            }
            end
            aria-label="Dashboard"
          >
            Dashboard
          </NavLink>
          <NavLink
            to="/trees"
            className={({ isActive }) =>
              `block px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                isActive
                  ? 'bg-purple-100 dark:bg-purple-900/30 text-purple-700 dark:text-purple-300'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 hover:text-gray-900 dark:hover:text-white'
              }`
            }
            aria-label="Trees"
          >
            Trees
          </NavLink>
          <NavLink
            to="/tree/demo"
            className={({ isActive }) =>
              `block px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                isActive
                  ? 'bg-purple-100 dark:bg-purple-900/30 text-purple-700 dark:text-purple-300'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 hover:text-gray-900 dark:hover:text-white'
              }`
            }
            aria-label="Tree View — demo tree"
          >
            🌳 Tree View
          </NavLink>
          <NavLink
            to="/nodes"
            className={({ isActive }) =>
              `block px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                isActive
                  ? 'bg-purple-100 dark:bg-purple-900/30 text-purple-700 dark:text-purple-300'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 hover:text-gray-900 dark:hover:text-white'
              }`
            }
            aria-label="Nodes"
          >
            Nodes
          </NavLink>
          <NavLink
            to="/topics"
            className={({ isActive }) =>
              `block px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                isActive
                  ? 'bg-purple-100 dark:bg-purple-900/30 text-purple-700 dark:text-purple-300'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 hover:text-gray-900 dark:hover:text-white'
              }`
            }
            aria-label="Topics"
          >
            Topics
          </NavLink>
          <NavLink
            to="/cards"
            className={({ isActive }) =>
              `block px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                isActive
                  ? 'bg-purple-100 dark:bg-purple-900/30 text-purple-700 dark:text-purple-300'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 hover:text-gray-900 dark:hover:text-white'
              }`
            }
            aria-label="Cards"
          >
            Cards
          </NavLink>
          <NavLink
            to="/approvals"
            className={({ isActive }) =>
              `block px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                isActive
                  ? 'bg-purple-100 dark:bg-purple-900/30 text-purple-700 dark:text-purple-300'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 hover:text-gray-900 dark:hover:text-white'
              }`
            }
            aria-label="Approvals"
          >
            Approvals
          </NavLink>
        </nav>
        <div className="p-4 border-t border-gray-200 dark:border-gray-800">
          <p className="text-xs text-gray-400 dark:text-gray-500">
            Hermes Canopy v0.1.0
          </p>
        </div>
      </aside>

      {/* Main content area */}
      <div className="flex-1 flex flex-col overflow-hidden">
        {/* Header */}
        <header
          className="h-14 bg-white dark:bg-gray-900 border-b border-gray-200 dark:border-gray-800 flex items-center px-6"
          role="banner"
        >
          <h2 className="text-sm font-medium text-gray-600 dark:text-gray-400">
            Knowledge Canopy
          </h2>
          <div className="ml-auto flex items-center gap-3">
            <span className="text-xs text-gray-400 dark:text-gray-500">
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
