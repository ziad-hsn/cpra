import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { useDashboardSession } from '../auth/SessionBoundary';
import { listOperations, operationMessage } from '../api/operations';
import { ManagementError } from '../api/session';
import { formatNumber } from '../lib/format';

const count = (value?: number) => value === undefined ? 'Not reported' : formatNumber(value);

export function OperationsList() {
  const session = useDashboardSession();
  const [filter, setFilter] = useState('');
  const [selection, setSelection] = useState({ monitorID: '', cursor: '', revision: 0, snapshot: '' });
  const valid = filter === '' || /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(filter);
  const query = useQuery({ queryKey: ['management', 'operations', 'list', selection],
    queryFn: async ({ signal }) => {
      const page = await listOperations(session, selection.cursor, selection.monitorID, signal);
      if (selection.cursor && selection.snapshot && page.snapshot !== selection.snapshot) throw new ManagementError('invalid', 'The operation snapshot changed during pagination.');
      return page;
    },
    enabled: session.can('ListOperations'), retry: false, gcTime: 0, staleTime: Infinity,
    refetchOnWindowFocus: false, refetchOnReconnect: false,
  });
  const page = query.data;
  const expired = query.error instanceof ManagementError && query.error.status === 410;
  const firstPage = () => setSelection(current => ({ monitorID: current.monitorID, cursor: '', revision: current.revision + 1, snapshot: '' }));
  return <section className="card" aria-labelledby="operations-list-heading">
    <h2 id="operations-list-heading">Retained operations</h2>
    <p>Browse one snapshot of resource changes and collection operations. Open an operation to read its latest progress.</p>
    <form className="management-form" aria-label="Filter operations" onSubmit={event => {
      event.preventDefault();
      if (valid) setSelection(current => ({ monitorID: filter, cursor: '', revision: current.revision + 1, snapshot: '' }));
    }}>
      <label className="management-field"><span>Exact monitor ID (optional)</span><input className="input" value={filter} onChange={event => setFilter(event.target.value)} /></label>
      {!valid && <p role="alert">Enter a valid monitor ID, or leave this field empty to include all resources.</p>}
      <button className="btn" type="submit" disabled={!valid || query.isFetching}>Apply filter</button>
    </form>
    {query.isPending && <p role="status">Reading operation page…</p>}
    {query.isError && <p role="alert">{expired ? 'This operation snapshot expired. Load a new first page to continue.' : 'Operation history is unavailable. This does not establish that earlier work failed or was never submitted.'}</p>}
    {page && <>
      <p className="muted">{page.generatedAt ? <>Snapshot taken <time dateTime={page.generatedAt}>{new Date(page.generatedAt).toLocaleString()}</time>.</> : 'Snapshot time is not reported.'} Current progress may have changed since this observation.</p>
      {page.items.length === 0 ? <p>{page.nextCursor ? 'No matching operations in this part of the snapshot. Continue to the next page.' : 'No retained operations in this part of the snapshot.'}</p> :
        <div className="table-scroll" role="region" aria-label="Retained operation page" tabIndex={0}><table className="data-table">
          <thead><tr><th>Operation</th><th>Work</th><th>State</th><th>Committed</th><th>Applied</th><th>Progress</th></tr></thead>
          <tbody>{page.items.map(operation => <tr key={operation.id}>
            <td className="mono">{session.can('GetOperation') ? <Link to={`/operations/${encodeURIComponent(operation.id)}`}>{operation.id}</Link> : operation.id}</td>
            <td>{operation.identityFormat !== undefined ? <>Collection · {count(operation.itemCount)} resources · {count(operation.uploaded)} uploaded</> : operation.items?.length ? <>{operation.items.slice(0, 3).map(item => item.id).join(', ')}{operation.items.length > 3 && `; ${operation.items.length - 3} additional items on this receipt page`}</> : 'Not reported'}</td>
            <td>{operation.state === 'unrecognized' ? 'Unrecognized state' : operation.state}</td>
            <td>{count(operation.committed)}</td><td>{count(operation.applied)}</td><td>{operationMessage(operation)}</td>
          </tr>)}</tbody>
        </table></div>}
    </>}
    <div className="row" style={{ gap: 12 }}>
      <button className="btn" disabled={query.isFetching} onClick={firstPage}>Refresh from first page</button>
      {page?.nextCursor && <button className="btn" disabled={query.isFetching || query.isError} onClick={() => setSelection(current => ({ ...current, cursor: page.nextCursor!, snapshot: page.snapshot ?? '' }))}>Next operation page</button>}
    </div>
    <p className="muted">Each page contains at most 100 receipts. Reading or refreshing this list does not repeat or cancel work.</p>
  </section>;
}
