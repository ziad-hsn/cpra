import { lazy, Suspense, useState } from 'react';
import { createBrowserRouter, RouterProvider, Routes, Route } from 'react-router-dom';
import { Layout } from './components/Layout';
import { LoadingSkeleton } from './components/LoadingSkeleton';
import { SessionBoundary } from './auth/SessionBoundary';
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
  return (
    <div className="card">
      <LoadingSkeleton lines={4} />
    </div>
  );
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
  const [router] = useState(() => createBrowserRouter([{ path: '*', element: <AppShell /> }]));
  return (
    <RouterProvider router={router} />
  );
}
