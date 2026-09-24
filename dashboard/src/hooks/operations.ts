import { useQuery } from '@tanstack/react-query';
import { useDashboardSession } from '../auth/SessionBoundary';
import { getOperation, operationID, operationPollInterval } from '../api/operations';

export function useOperation(id?: string) {
  const session = useDashboardSession();
  return useQuery({ queryKey: ['management', 'operation', id], queryFn: async ({ signal }) => {
    const response = await getOperation(session, id!, signal);
    if (response.data.identityFormat !== undefined && response.data.executionResult !== undefined) {
      // The explicit result reader owns the only retained collection item page.
      const data = { ...response.data }; delete data.items; delete data.nextCursor;
      return { ...response, data };
    }
    return response;
  },
    enabled: operationID(id) && session.can('GetOperation'), gcTime: 0, retry: false,
    refetchInterval: query => operationPollInterval(query.state.data, query.state.error),
  });
}
