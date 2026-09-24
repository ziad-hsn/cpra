import { workerCollectionLimits } from '../api/collectionInventory';
import { CollectionImportError } from './protocol';

/** Names remain local. Their UTF-8 byte ordering matches Go's string ordering.
 * A repeated selected name is rejected, including equal names with equal bytes. */
export interface SelectedCollectionSource { readonly name: string; readonly file: File }
export function collectionFileSources(files: readonly File[]): SelectedCollectionSource[] {
  if (!Array.isArray(files)) throw new CollectionImportError('invalid');
  return orderedCollectionSources(files.map(file => { if (!(file instanceof File)) throw new CollectionImportError('invalid'); return { file, name: file.webkitRelativePath || file.name }; }));
}
export function orderedCollectionSources(sources: readonly SelectedCollectionSource[]): SelectedCollectionSource[] {
  if (!Array.isArray(sources) || sources.length < 1 || sources.length > workerCollectionLimits.sources) throw new CollectionImportError('quota');
  let size = 0;
  const names = new Set<string>();
  const encoded = sources.map(({ file, name }) => {
    if (!(file instanceof File) || !Number.isSafeInteger(file.size) || file.size < 0) throw new CollectionImportError('invalid');
    if (typeof name !== 'string') throw new CollectionImportError('invalid');
    const key = new TextEncoder().encode(name);
    if (key.length === 0 || key.length > 4096 || /[\\\x00-\x1f\x7f]/.test(name) || name.startsWith('/') || name.split('/').some(part => part === '' || part === '.' || part === '..') ||
        new TextDecoder('utf-8', { fatal: true }).decode(key) !== name || !/\.(?:ya?ml|json)$/i.test(name)) throw new CollectionImportError('invalid');
    if (names.has(name)) throw new CollectionImportError('duplicate');
    names.add(name); size += file.size;
    if (size > workerCollectionLimits.sourceBytes) throw new CollectionImportError('quota');
    return { file, name, key };
  });
  encoded.sort((a, b) => { for (let i = 0; i < Math.min(a.key.length, b.key.length); i++) { const delta = a.key[i] - b.key[i]; if (delta) return delta; } return a.key.length - b.key.length; });
  return encoded.map(({ file, name }) => ({ file, name }));
}
