import type { SelectedCollectionSource } from './files';
import type { WorkerCollectionItemSummary, WorkerCollectionSummary } from '../api/collectionInventory';

export type ImportErrorCode = 'invalid' | 'quota' | 'duplicate' | 'unavailable' | 'closed' | 'cancelled';
export interface ImportLocation { source?: number; document?: number; item?: number }
export type WorkerRequest = { id: number; kind: 'prepare'; sources: SelectedCollectionSource[] } | { id: number; kind: 'page'; offset: number; limit: number }
  | { id: number; kind: 'create' | 'preflight' } | { id: number; kind: 'upload'; offset: number };
export type WorkerReply = { kind: 'ready' } | { kind: 'progress'; phase: 'parsing' | 'parsed' | 'identity'; sources: number; items: number }
  | { id: number; kind: 'error'; code: ImportErrorCode; location?: ImportLocation }
  | { id: number; kind: 'prepared'; summary: WorkerCollectionSummary }
  | { id: number; kind: 'page'; items: WorkerCollectionItemSummary[] }
  | { id: number; kind: 'body'; body: ArrayBuffer; nextOffset?: number };
export const importErrorMessages: Record<ImportErrorCode, string> = {
  invalid: 'The selected collection is invalid. No changes were submitted.',
  quota: 'The collection exceeds a browser import quota. No changes were submitted.',
  duplicate: 'The selection contains duplicate source names or resource identities. No changes were submitted.',
  unavailable: 'The private import worker could not prepare the collection.',
  closed: 'The private import has been closed. Select the files again.',
  cancelled: 'Collection preparation was cancelled. No changes were submitted.',
};

export class CollectionImportError extends Error {
  readonly code: ImportErrorCode;
  readonly location?: Readonly<ImportLocation>;
  constructor(code: ImportErrorCode, location?: ImportLocation) { super(importErrorMessages[code]); this.name = 'CollectionImportError'; this.code = code; this.location = location ? Object.freeze({ ...location }) : undefined; }
}
