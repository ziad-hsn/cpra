import { lazy, Suspense } from 'react';
import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Layout } from './components/Layout';
import { LoadingSkeleton } from './components/LoadingSkeleton';
import './styles/global.css';

const Overview = lazy(() => import('./pages/Overview'));
const Monitors = lazy(() => import('./pages/Monitors'));
const MonitorDetail = lazy(() => import('./pages/MonitorDetail'));
const Alerts = lazy(() => import('./pages/Alerts'));
const SystemHealth = lazy(() => import('./pages/SystemHealth'));
const Settings = lazy(() => import('./pages/Settings'));

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

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
        <Route path="/alerts" element={<Alerts />} />
        <Route path="/system" element={<SystemHealth />} />
        <Route path="/settings" element={<Settings />} />
      </Routes>
    </Suspense>
  );
}

/** AppShell — QueryClientProvider + Layout + routes (no router). */
export function AppShell() {
  return (
    <QueryClientProvider client={queryClient}>
      <Layout>
        <AppRoutes />
      </Layout>
    </QueryClientProvider>
  );
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Layout>
          <AppRoutes />
        </Layout>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
