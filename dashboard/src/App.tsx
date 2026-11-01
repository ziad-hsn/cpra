import { lazy, Suspense, useState } from 'react';
import { onlineManager } from '@tanstack/react-query';
import { createBrowserRouter, RouterProvider, Routes, Route, Link, useLocation } from 'react-router-dom';
import { Layout } from './components/Layout';
import { LoadingSkeleton } from './components/LoadingSkeleton';
import { SessionBoundary } from './auth/SessionBoundary';
import { useOnlineStatus } from './hooks/connection';
import { PageBoundary } from './components/PageBoundary';
import './styles/global.css';
import './styles/management.css';

function importPage<T>(load: () => Promise<T>): Promise<T> {
  // Avoid poisoning the browser module cache when offline is already known.
  // An explicit retry creates a new lazy wrapper and can load after reconnect.
  if (!onlineManager.isOnline()) return Promise.reject(new Error('Page unavailable while offline'));
  return load();
}

function createPages() {
  return {
    Overview: lazy(() => importPage(() => import('./pages/Overview'))),
    Monitors: lazy(() => importPage(() => import('./pages/Monitors'))),
    MonitorDetail: lazy(() => importPage(() => import('./pages/MonitorDetail'))),
    MonitorState: lazy(() => importPage(() => import('./pages/MonitorState'))),
    Alerts: lazy(() => importPage(() => import('./pages/Alerts'))),
    SystemHealth: lazy(() => importPage(() => import('./pages/SystemHealth'))),
    Settings: lazy(() => importPage(() => import('./pages/Settings'))),
    OperationDetail: lazy(() => importPage(() => import('./pages/OperationDetail'))),
    ManagementResources: lazy(() => importPage(() => import('./pages/ManagementResources'))),
    CollectionImport: lazy(() => importPage(() => import('./pages/CollectionImport'))),
  };
}

function PageFailure({retry, exhausted}: {retry: () => void; exhausted: boolean}) {
  const online = useOnlineStatus();
  return <section className="page route-failure">
    <div className="page-head"><h1>{online ? 'This page could not be loaded' : 'You are offline'}</h1></div>
    <p>{online ? 'Navigation and your current sign-in remain available.' : 'Reconnect before retrying. Saved observations may be stale.'}</p>
    <p>Retrying this page does not repeat submitted changes. Check the original operation before submitting a change again.</p>
    <div className="row"><button className="btn" disabled={!online || exhausted} onClick={retry}>Retry page</button><Link className="btn" to="/">Go to overview</Link></div>
    {exhausted && <p>Page retries are exhausted. You can use another page or reload the dashboard and sign in again.</p>}
  </section>;
}

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
  const {pathname} = useLocation();
  const [pages, setPages] = useState(createPages);
  const [retryState, setRetryState] = useState({path: '', count: 0, generation: 0});
  const count = retryState.path === pathname ? retryState.count : 0;
  const {Overview, Monitors, MonitorDetail, MonitorState, Alerts, SystemHealth, Settings, OperationDetail, ManagementResources, CollectionImport} = pages;
  const retry = () => {
    if (count >= 3) return;
    // React.lazy caches a rejected promise. A fresh wrapper is necessary for
    // an explicit retry; the surrounding session and caches are untouched.
    setPages(createPages());
    setRetryState({path: pathname, count: count + 1, generation: retryState.generation + 1});
  };
  return (
    <PageBoundary resetKey={`${pathname}:${retryState.generation}`} fallback={<PageFailure retry={retry} exhausted={count >= 3} />}>
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
    </PageBoundary>
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
