import { useSyncExternalStore } from 'react';
import { onlineManager } from '@tanstack/react-query';

/** Observe the same connectivity signal that pauses read queries. */
export function useOnlineStatus() {
  return useSyncExternalStore(
    listener => onlineManager.subscribe(listener),
    () => onlineManager.isOnline(),
    () => true,
  );
}
