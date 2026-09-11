import { create } from 'zustand';

export type ThemeMode = 'dark' | 'light';

interface ThemeStore {
  theme: ThemeMode;
  toggleTheme: () => void;
  setTheme: (t: ThemeMode) => void;
}

const STORAGE_KEY = 'cpra-theme';

function getInitialTheme(): ThemeMode {
  if (typeof window !== 'undefined') {
    try {
      const stored = localStorage.getItem(STORAGE_KEY);
      if (stored === 'dark' || stored === 'light') return stored;
    } catch { /* Storage may be unavailable in a restricted browser. */ }
  }
  return 'light';
}

function applyTheme(theme: ThemeMode): void {
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-theme', theme);
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#15161a' : '#fbf3df');
  }
  if (typeof window !== 'undefined') {
    try {
      localStorage.setItem(STORAGE_KEY, theme);
    } catch { /* The theme can still be changed for this session. */ }
  }
}

export const useTheme = create<ThemeStore>((set, get) => {
  const initial = getInitialTheme();
  applyTheme(initial);
  return {
    theme: initial,
    toggleTheme: () => {
      const next: ThemeMode = get().theme === 'dark' ? 'light' : 'dark';
      applyTheme(next);
      set({ theme: next });
    },
    setTheme: (t) => {
      applyTheme(t);
      set({ theme: t });
    },
  };
});
