import { create } from 'zustand';
import palette from '../../../brand/palette.json';

export type ThemeMode = 'dark' | 'light';
export type ThemePreference = ThemeMode | 'system';

interface ThemeStore {
  theme: ThemeMode;
  preference: ThemePreference;
  toggleTheme: () => void;
  setTheme: (preference: ThemePreference) => void;
}

const STORAGE_KEY = 'cpra-theme';
const systemAppearance = typeof window !== 'undefined' && typeof window.matchMedia === 'function'
  ? window.matchMedia('(prefers-color-scheme: dark)') : undefined;

function initialPreference(): ThemePreference {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === 'dark' || stored === 'light') return stored;
  } catch { /* Follow system appearance when storage is unavailable. */ }
  return 'system';
}

function resolveTheme(preference: ThemePreference): ThemeMode {
  return preference === 'system' ? (systemAppearance?.matches ? 'dark' : 'light') : preference;
}

function applyTheme(theme: ThemeMode): void {
  if (typeof document !== 'undefined') {
    document.documentElement.dataset.theme = theme;
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', palette[theme].canvas);
  }
}

export const useTheme = create<ThemeStore>((set, get) => {
  const preference = initialPreference();
  const theme = resolveTheme(preference);
  applyTheme(theme);
  return {
    theme,
    preference,
    toggleTheme: () => get().setTheme(get().theme === 'dark' ? 'light' : 'dark'),
    setTheme: (next) => {
      const resolved = resolveTheme(next);
      applyTheme(resolved);
      set({ theme: resolved, preference: next });
      try {
        localStorage.setItem(STORAGE_KEY, next);
      } catch { /* Theme selection still works for this session. */ }
    },
  };
});

function syncSystemAppearance(): void {
  if (useTheme.getState().preference !== 'system') return;
  const theme = resolveTheme('system');
  applyTheme(theme);
  useTheme.setState({ theme });
}

systemAppearance?.addEventListener('change', syncSystemAppearance);
if (import.meta.hot) {
  import.meta.hot.dispose(() => systemAppearance?.removeEventListener('change', syncSystemAppearance));
}
