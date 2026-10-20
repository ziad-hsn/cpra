import { createContext, useContext, useId, type InputHTMLAttributes, type ReactNode } from 'react';
import type { ManagementError } from '../api/session';
import { safeValidationField, validationMessage } from '../api/validation';

const Validation = createContext<{ prefix: string; errors: NonNullable<NonNullable<ManagementError['problem']>['errors']> }>({ prefix: '', errors: [] });
const canonical = (path: string) => safeValidationField(path).replace(/_/g, '').toLowerCase();

export function FieldValidation({ error, children }: { error?: ManagementError; children: ReactNode }) {
  const prefix = useId();
  return <Validation.Provider value={{ prefix, errors: (error?.problem?.errors ?? []).slice(0, 10) }}>{children}</Validation.Provider>;
}

export function FieldInput({ field, ...props }: InputHTMLAttributes<HTMLInputElement> & { field: string }) {
  const validation = useFieldValidation();
  return <><input {...validation.attributes(field)} {...props} />{validation.message(field)}</>;
}

/** IDs derive only from reviewed schema paths, never submitted field values. */
export function useFieldValidation() {
  const { prefix, errors } = useContext(Validation);
  const id = (path: string) => prefix && canonical(path) ? `${prefix}-field-${canonical(path)}` : undefined;
  const issue = (path: string) => errors.find(error => canonical(path) && canonical(error.field ?? '') === canonical(path));
  return {
    attributes(path = '') {
      const target = id(path), error = issue(path);
      return { id: target, 'aria-invalid': error ? true as const : undefined, 'aria-describedby': target && error ? `${target}-error` : undefined };
    },
    message(path = '') {
      const error = issue(path), target = id(path);
      // The input references this text as its description. Exclude it from an
      // enclosing label's name so errors do not rename the editable control.
      return error && target ? <span id={`${target}-error`} className="field-error" aria-hidden="true">{validationMessage(error.reason) || 'This field did not pass validation.'}</span> : null;
    },
    target(path: string) {
      if (typeof document === 'undefined') return undefined;
      // A parent object or array error may belong to its one editable control.
      for (let current = path; current;) {
        const target = id(current);
        if (target && document.getElementById(target)) return target;
        const end = Math.max(current.lastIndexOf('.'), current.lastIndexOf('['));
        current = end < 0 ? '' : current.slice(0, end);
      }
      return undefined;
    },
  };
}
