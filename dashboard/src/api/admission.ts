const object = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/** Error bodies alone use this bounded, duplicate-aware UTF-8 decoder. */
export function decodeErrorJSON(bytes: Uint8Array): unknown {
  if (bytes.byteLength > 64 * 1024) throw new Error('Error response exceeds its bound');
  const text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
  const decoded: unknown = JSON.parse(text);
  // JSON.parse establishes syntax; this pass rejects duplicate decoded names,
  // including escaped aliases, without building a second resource tree.
  let offset = 0;
  const whitespace = () => { while (offset < text.length && /[ \t\r\n]/.test(text[offset])) offset++; };
  const quoted = (): string => {
    const start = offset++;
    while (offset < text.length) {
      const character = text[offset++];
      if (character === '\\') offset++;
      else if (character === '"') return JSON.parse(text.slice(start, offset)) as string;
    }
    throw new Error('Invalid error response');
  };
  const value = (depth: number): void => {
    if (depth > 128) throw new Error('Error response nesting exceeds its bound');
    whitespace();
    const character = text[offset];
    if (character === '{') {
      offset++;
      whitespace();
      const names = new Set<string>();
      while (text[offset] !== '}') {
        const name = quoted();
        if (names.has(name)) throw new Error('Duplicate error response field');
        names.add(name);
        whitespace();
        offset++; // The already validated colon.
        value(depth + 1);
        whitespace();
        if (text[offset] !== ',') break;
        offset++;
        whitespace();
      }
      offset++;
    } else if (character === '[') {
      offset++;
      whitespace();
      while (text[offset] !== ']') {
        value(depth + 1);
        whitespace();
        if (text[offset] !== ',') break;
        offset++;
      }
      offset++;
    } else if (character === '"') quoted();
    else while (offset < text.length && !/[ \t\r\n,}\]]/.test(text[offset])) offset++;
  };
  value(0);
  return decoded;
}

// Parse the relevant MIME grammar, including quoted parameters. Splitting on
// semicolons would accept malformed values and merged duplicate headers.
function problemMediaType(header: string | null): boolean {
  if (header === null) return false;
  let offset = 0;
  const whitespace = () => { while (offset < header.length && /[ \t]/.test(header[offset])) offset++; };
  const token = () => {
    const start = offset;
    while (offset < header.length && /[!#$%&'*+.^_`|~0-9A-Za-z-]/.test(header[offset])) offset++;
    return header.slice(start, offset);
  };
  whitespace();
  if (token().toLowerCase() !== 'application' || header[offset++] !== '/' || token().toLowerCase() !== 'problem+json') return false;
  const parameters = new Map<string, string>();
  while (offset < header.length) {
    whitespace();
    if (offset === header.length) return true;
    if (header[offset++] !== ';') return false;
    whitespace();
    if (offset === header.length) return true; // Go's MIME parser accepts a trailing semicolon.
    const name = token().toLowerCase();
    whitespace();
    if (!name || header[offset++] !== '=') return false;
    whitespace();
    let parameter = '';
    if (header[offset] === '"') {
      offset++;
      let closed = false;
      while (offset < header.length) {
        let character = header[offset++];
        if (character === '"') { closed = true; break; }
        if (character === '\\') character = header[offset++];
        if (character === undefined || !/[\t\x20-\x7e]/.test(character)) return false;
        parameter += character;
      }
      if (!closed) return false;
    } else {
      parameter = token();
      if (!parameter) return false;
    }
    if (parameters.has(name) && parameters.get(name) !== parameter) return false;
    parameters.set(name, parameter);
  }
  return true;
}

/** Only this validated contract proves that target admission did not occur. */
export function allocationNotSubmitted(response: Response, value: unknown): boolean {
  if (response.status !== 503 || response.headers.has('X-Operation-ID') || response.headers.get('X-CPRa-Admission') !== 'not-submitted' || !problemMediaType(response.headers.get('Content-Type')) || !object(value)) return false;
  if (value.type !== 'about:blank' || value.status !== 503 || value.code !== 'operationAllocationUnconfirmed' || typeof value.title !== 'string' || !value.title.trim()) return false;
  for (const [name, field] of Object.entries(value)) {
    if (name === 'status') continue;
    if (['type', 'title', 'detail', 'instance', 'code', 'requestID'].includes(name)) {
      if (typeof field !== 'string') return false;
    } else if (name === 'errors') {
      if (!Array.isArray(field) || !field.every(error => object(error) && typeof error.field === 'string' && typeof error.message === 'string' &&
        Object.entries(error).every(([key, item]) => ['field', 'message', 'reason'].includes(key) && typeof item === 'string'))) return false;
    } else return false; // Includes operationID, even an empty or null value.
  }
  return true;
}
