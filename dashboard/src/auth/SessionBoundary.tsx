import { createContext, useContext, useEffect, useState, useSyncExternalStore, type FormEvent, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { DashboardSession, ManagementError, dashboardSession } from '../api/session';

const SessionContext = createContext(dashboardSession);
export const useDashboardSession = () => useContext(SessionContext);

export function useAccess() {
  const session = useDashboardSession();
  return useSyncExternalStore(session.subscribe, session.getSnapshot);
}

/** A fresh cache per identity also prevents placeholder data crossing principals. */
function IdentityQueries({ session, children }: { session: DashboardSession; children: ReactNode }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: {
    queries: {
      // Retry only transient reads. Invalid identities, oversized bodies and
      // rejected requests need operator attention, not another automatic read.
      retry: (count, error) => count < 1 && (!(error instanceof ManagementError) || error.reason === 'unavailable' || (error.reason === 'http' && (error.status ?? 0) >= 500)),
      refetchOnWindowFocus: false,
    },
    // A failed write is never paused for reconnect, queued, retried, or persisted.
    mutations: { retry: false, networkMode: 'always', gcTime: 0 },
  } }));
  useEffect(() => {
    const clear = () => { void client.cancelQueries(); client.clear(); };
    const unsubscribe = session.onReset(clear);
    return () => { unsubscribe(); clear(); };
  }, [client, session]);
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function SignIn({ session }: { session: DashboardSession }) {
  const state = useSyncExternalStore(session.subscribe, session.getSnapshot);
  const [token, setToken] = useState('');
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (state.phase === 'signing-in') return;
    const supplied = token;
    setToken('');
    void session.signIn(supplied);
  };
  return <main className="page" style={{ maxWidth: 560, margin: '48px auto', width: '100%' }}>
    <h1>Sign in to CPRa</h1>
    <p>Use the bearer token provided by your CPRa administrator. Notification contacts do not grant dashboard access.</p>
    <form className="card" onSubmit={submit} aria-label="Dashboard sign-in">
      <label htmlFor="cpra-access-token">Bearer token</label>
      <input id="cpra-access-token" className="input" type="password" value={token}
        autoComplete="off" autoCapitalize="none" spellCheck={false}
        onChange={event => setToken(event.target.value)} disabled={state.phase === 'signing-in'}
        aria-describedby="cpra-token-note" style={{ width: '100%', margin: '8px 0 16px' }} />
      <p id="cpra-token-note" className="muted">Your token stays in this tab's memory. Refreshing or closing the tab requires sign-in again.</p>
      {state.message && <p role="alert">{state.message}</p>}
      <button className="btn" type="submit" disabled={!token || state.phase === 'signing-in'}>
        {state.phase === 'signing-in' ? 'Signing in…' : 'Sign in'}
      </button>
    </form>
  </main>;
}

export function SessionBoundary({ children, session = dashboardSession }: { children: ReactNode; session?: DashboardSession }) {
  const state = useSyncExternalStore(session.subscribe, session.getSnapshot);
  useEffect(() => { void session.discover(); }, [session]);

  if (state.phase === 'discovering') return <main className="page"><h1>CPRa</h1><p role="status">Connecting to your server…</p></main>;
  if (state.phase === 'unavailable') return <main className="page"><h1>CPRa</h1><p role="alert">{state.message}</p><button className="btn" onClick={() => void session.retryDiscovery()}>Retry connection</button></main>;
  if (state.phase === 'signed-out' || state.phase === 'signing-in') return <SignIn key={state.epoch} session={session} />;

  return <SessionContext.Provider value={session}>
    <IdentityQueries key={state.epoch} session={session}>
      <div className="row" aria-label="Dashboard access" style={{ padding: '8px 16px', justifyContent: 'space-between', background: 'var(--surface-2)' }}>
        {state.phase === 'legacy' ? <span>Read-only server. Management is not available on this server version.</span>
          : <><span>{state.access?.principalId} · {state.access?.role === 'operator' ? 'Operator' : 'Read-only access'}</span><button className="btn ghost" onClick={session.signOut}>Sign out</button></>}
      </div>
      {children}
    </IdentityQueries>
  </SessionContext.Provider>;
}
