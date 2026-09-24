import { afterEach, beforeEach, expect, it, vi } from 'vitest';

let dark = true;
let onChange: (() => void) | undefined;
beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
  dark = true;
  onChange = undefined;
  vi.stubGlobal('matchMedia', vi.fn(() => ({
    get matches() { return dark; },
    addEventListener: (_event: string, listener: () => void) => { onChange = listener; },
    removeEventListener: vi.fn(),
  })));
  document.head.innerHTML = '<meta name="theme-color" content="">';
});
afterEach(() => vi.unstubAllGlobals());

it('starts in system mode and follows appearance changes without saving an override', async () => {
  const { useTheme } = await import('../src/theme/useTheme');
  expect(useTheme.getState()).toMatchObject({ preference: 'system', theme: 'dark' });
  expect(document.documentElement.dataset.theme).toBe('dark');
  expect(localStorage.getItem('cpra-theme')).toBeNull();
  dark = false;
  onChange?.();
  expect(useTheme.getState().theme).toBe('light');
  expect(document.documentElement.dataset.theme).toBe('light');
  expect(document.querySelector('meta[name="theme-color"]')?.getAttribute('content')).toBe('#F7F8FA');
});

it('keeps an explicit choice through system changes and reloads', async () => {
  localStorage.setItem('cpra-theme', 'light');
  const { useTheme } = await import('../src/theme/useTheme');
  expect(useTheme.getState()).toMatchObject({ preference: 'light', theme: 'light' });
  onChange?.();
  expect(useTheme.getState().theme).toBe('light');
  useTheme.getState().toggleTheme();
  expect(localStorage.getItem('cpra-theme')).toBe('dark');
  dark = false;
  onChange?.();
  expect(useTheme.getState().theme).toBe('dark');
  vi.resetModules();
  const reloaded = await import('../src/theme/useTheme');
  expect(reloaded.useTheme.getState()).toMatchObject({ preference: 'dark', theme: 'dark' });
});

it('returns to the current system appearance when System is selected', async () => {
  const { useTheme } = await import('../src/theme/useTheme');
  useTheme.getState().setTheme('light');
  useTheme.getState().setTheme('system');
  expect(localStorage.getItem('cpra-theme')).toBe('system');
  expect(useTheme.getState().theme).toBe('dark');
  dark = false;
  onChange?.();
  expect(useTheme.getState().theme).toBe('light');
});

it('still applies theme changes if browser storage is blocked', async () => {
  const read = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
  const write = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
  try {
    const { useTheme } = await import('../src/theme/useTheme');
    expect(useTheme.getState().theme).toBe('dark');
    useTheme.getState().setTheme('light');
    expect(document.documentElement.dataset.theme).toBe('light');
  } finally {
    read.mockRestore();
    write.mockRestore();
  }
});
