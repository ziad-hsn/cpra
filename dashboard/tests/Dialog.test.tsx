import { StrictMode, useState } from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { ConfirmDialog } from '../src/components/ConfirmDialog';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function Fixture({ pending = false, confirmDisabled = false }: { pending?: boolean; confirmDisabled?: boolean }) {
  const [open, setOpen] = useState(false);
  return <>
    <button onClick={() => setOpen(true)}>Delete monitor</button>
    <main id="main-content" tabIndex={-1}>Monitor state</main>
    {open && <ConfirmDialog title="Delete checkout?" pending={pending} confirmDisabled={confirmDisabled} onCancel={() => setOpen(false)} onConfirm={() => setOpen(false)}>
      <p>Recorded history remains available.</p>
    </ConfirmDialog>}
  </>;
}

it('focuses the safe choice and restores the trigger when a draft is cancelled', async () => {
  render(<Fixture />);
  const trigger = screen.getByRole('button', { name: 'Delete monitor' });
  trigger.focus(); fireEvent.click(trigger);
  const dialog = screen.getByRole('alertdialog', { name: 'Delete checkout?' });
  expect(dialog).toHaveAttribute('open');
  expect(within(dialog).getByRole('button', { name: 'Keep resource' })).toHaveFocus();
  fireEvent(dialog, new Event('cancel', { cancelable: true }));
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  await waitFor(() => expect(trigger).toHaveFocus());
});

it('blocks native close requests while a mutation is pending without reopening the draft', () => {
  const { rerender } = render(<Fixture />);
  fireEvent.click(screen.getByRole('button', { name: 'Delete monitor' }));
  const dialog = screen.getByRole('alertdialog');
  rerender(<Fixture pending />);
  const request = new Event('cancel', { cancelable: true });
  fireEvent(dialog, request);
  expect(request.defaultPrevented).toBe(true);
  expect(dialog).toBeInTheDocument();
  expect(dialog).toHaveAttribute('aria-busy', 'true');
  expect(within(dialog).getByRole('button', { name: 'Deleting…' })).toBeDisabled();
  rerender(<Fixture />);
  expect(screen.getByRole('alertdialog')).toBe(dialog);
  fireEvent(dialog, new Event('cancel', { cancelable: true }));
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
});

it('keeps cancellation available when confirmation is not authorized', () => {
  render(<Fixture confirmDisabled />);
  fireEvent.click(screen.getByRole('button', { name: 'Delete monitor' }));
  const dialog = screen.getByRole('alertdialog');
  expect(within(dialog).getByRole('button', { name: 'Confirm deletion' })).toBeDisabled();
  expect(within(dialog).getByRole('button', { name: 'Keep resource' })).toBeEnabled();
  fireEvent.click(within(dialog).getByRole('button', { name: 'Keep resource' }));
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
});

it('does not submit a surrounding editor form from either confirmation button', () => {
  const submit = vi.fn();
  const confirm = vi.fn();
  const cancel = vi.fn();
  render(<form onSubmit={event => { event.preventDefault(); submit(); }}>
    <ConfirmDialog title="Delete resource?" pending={false} onCancel={cancel} onConfirm={confirm}><p>Conditional deletion.</p></ConfirmDialog>
  </form>);
  fireEvent.click(screen.getByRole('button', { name: 'Keep resource' }));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm deletion' }));
  expect(cancel).toHaveBeenCalledOnce(); expect(confirm).toHaveBeenCalledOnce(); expect(submit).not.toHaveBeenCalled();
});

it('returns to main content if the original trigger no longer exists', async () => {
  const { rerender } = render(<><button>Original trigger</button><main id="main-content" tabIndex={-1}>State</main></>);
  screen.getByRole('button', { name: 'Original trigger' }).focus();
  rerender(<><button>Original trigger</button><main id="main-content" tabIndex={-1}>State</main><ConfirmDialog title="Delete resource?" pending={false} onCancel={() => undefined} onConfirm={() => undefined}>History remains.</ConfirmDialog></>);
  rerender(<main id="main-content" tabIndex={-1}>State</main>);
  await waitFor(() => expect(screen.getByRole('main')).toHaveFocus());
});

it('remains usable after Strict Mode reopens effects', () => {
  render(<StrictMode><Fixture /></StrictMode>);
  fireEvent.click(screen.getByRole('button', { name: 'Delete monitor' }));
  const dialog = screen.getByRole('alertdialog');
  expect(dialog).toHaveAttribute('open');
  expect(within(dialog).getByRole('button', { name: 'Keep resource' })).toHaveFocus();
  fireEvent.click(within(dialog).getByRole('button', { name: 'Keep resource' }));
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
});
