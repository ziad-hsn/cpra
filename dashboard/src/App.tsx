import { lazy, Suspense, useState } from 'react';
import { createBrowserRouter, RouterProvider, Routes, Route, Link } from 'react-router-dom';
import { Layout } from './components/Layout';
import { LoadingSkeleton } from './components/LoadingSkeleton';
import { SessionBoundary } from './auth/SessionBoundary';
import { useOnlineStatus } from './hooks/connection';
import './styles/global.css';
import './styles/management.css';

const Overview = lazy(() => import('./pages/Overview'));
const Monitors = lazy(() => import('./pages/Monitors'));
const MonitorDetail = lazy(() => import('./pages/MonitorDetail'));
const MonitorState = lazy(() => import('./pages/MonitorState'));
const Alerts = lazy(() => import('./pages/Alerts'));
const SystemHealth = lazy(() => import('./pages/SystemHealth'));
const Settings = lazy(() => import('./pages/Settings'));
const OperationDetail = lazy(() => import('./pages/OperationDetail'));
const ManagementResources = lazy(() => import('./pages/ManagementResources'));
const CollectionImport = lazy(() => import('./pages/CollectionImport'));

function PageFallback() {
  const online = useOnlineStatus();
  return (
    <div className="card">
      {online ? <LoadingSkeleton lines={4} /> : <p role="status">You are offline. Reconnect to load this page.</p>}
    </div>
  );
}

/** Rendering and lazy-module failures must never expose a stack or error payload. */
export function RouteFailure() {
  const online = useOnlineStatus();
  return <main className="page route-failure">
    <div className="page-head"><h1>{online ? 'This page could not be loaded' : 'You are offline'}</h1></div>
    <p>{online ? 'The dashboard could not display this page. Reload to try again.' : 'Reconnect, then reload the dashboard. Current monitor health is unavailable.'}</p>
    <p>Reloading does not repeat submitted changes. Check the original operation or resource before trying a change again. You may need to sign in again.</p>
    <div className="row"><button className="btn" onClick={() => window.location.reload()}>Reload dashboard</button><a className="btn" href="/">Go to overview</a></div>
  </main>;
}

function NotFound() {
  return <div className="page">
    <div className="page-head"><h1>Page not found</h1></div>
    <p>This dashboard page does not exist. Choose a page from the navigation or return to the overview.</p>
    <Link to="/" className="btn">Go to overview</Link>
  </div>;
}

/** AppRoutes — the routes without a router wrapper, for testing. */
export function AppRoutes() {
  return (
    <Suspense fallback={<PageFallback />}>
      <Routes>
        <Route path="/" element={<Overview />} />
        <Route path="/monitors" element={<Monitors />} />
        <Route path="/monitors/:id" element={<MonitorDetail />} />
        <Route path="/monitors/by-id/:monitorID" element={<MonitorState />} />
        <Route path="/alerts" element={<Alerts />} />
        <Route path="/system" element={<SystemHealth />} />
        <Route path="/operations/:id?" element={<OperationDetail />} />
        <Route path="/import" element={<CollectionImport />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/monitor-configurations/:id?" element={<ManagementResources key="monitors" type="monitors" />} />
        <Route path="/recipients/:id?" element={<ManagementResources key="recipients" type="recipients" />} />
        <Route path="/notification-endpoints/:id?" element={<ManagementResources key="endpoints" type="endpoints" />} />
        <Route path="/notification-groups/:id?" element={<ManagementResources key="groups" type="groups" />} />
        <Route path="/secrets/:id?" element={<ManagementResources key="credentials" type="credentials" />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
    </Suspense>
  );
}

/** AppShell — access boundary, a cache per identity, layout and routes (no router). */
export function AppShell() {
  return (
    <SessionBoundary>
      <Layout>
        <AppRoutes />
      </Layout>
    </SessionBoundary>
  );
}

export default function App() {
  const [router] = useState(() => createBrowserRouter([{ path: '*', element: <AppShell />, errorElement: <RouteFailure /> }]));
  return (
    <RouterProvider router={router} />
  );
}
