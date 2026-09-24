import { useCallback, useEffect, useRef, useState } from 'react';
import { useDashboardSession } from '../auth/SessionBoundary';
import { ManagementError, type APIResponse, type MutationOptions } from '../api/session';

interface MutationState<T> {
  pending: boolean;
  response?: APIResponse<T>;
  error?: ManagementError;
}

/**
 * Execute explicit user actions directly, without TanStack's mutation-variable
 * cache or offline queues. Request bodies (including secret values) stay out of
 * hook state. Cancelling stops waiting; it cannot undo a server admission.
 */
export function useManagementMutation<T>(sanitize?: (response: APIResponse<T>) => APIResponse<T>) {
  const session = useDashboardSession();
  const [state, setState] = useState<MutationState<T>>({ pending: false });
  const active = useRef<AbortController | undefined>(undefined);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    const unsubscribe = session.onReset(() => {
      active.current?.abort();
      active.current = undefined;
      setState({ pending: false });
    });
    return () => { mounted.current = false; unsubscribe(); active.current?.abort(); };
  }, [session]);

  const execute = useCallback(async (path: string, options: MutationOptions): Promise<APIResponse<T>> => {
    if (active.current) throw new ManagementError('invalid', 'An operation is already being submitted.');
    const controller = new AbortController();
    active.current = controller;
    const epoch = session.getSnapshot().epoch;
    setState({ pending: true });
    const abort = () => controller.abort();
    options.signal?.addEventListener('abort', abort, { once: true });
    if (options.signal?.aborted) controller.abort();
    const current = () => mounted.current && session.getSnapshot().epoch === epoch && active.current === controller;
    try {
      const received = await session.mutate<T>(path, { ...options, signal: controller.signal });
      const response = sanitize ? sanitize(received) : received;
      if (current()) setState({ pending: false, response });
      return response;
    } catch (error) {
      if (current()) setState({ pending: false, error: error instanceof ManagementError ? error : new ManagementError('unavailable', 'The request failed.') });
      throw error;
    } finally {
      options.signal?.removeEventListener('abort', abort);
      if (active.current === controller) active.current = undefined;
    }
  }, [session, sanitize]);

  return { ...state, execute, cancel: () => active.current?.abort(), clearFeedback: () => setState({ pending: active.current !== undefined }) };
}
