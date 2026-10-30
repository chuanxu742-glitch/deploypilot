export type ThemeName = "day" | "night" | "comfort";

const THEMES: ThemeName[] = ["day", "night", "comfort"];

export function resolveTheme(saved: string | null): ThemeName {
  if (saved && THEMES.includes(saved as ThemeName)) return saved as ThemeName;
  return "night";
}

export function clampSplitRatio(value: number, usableWidth?: number): number {
  if (!Number.isFinite(value)) return 0.6;
  const max = usableWidth
    ? Math.max(0.4, Math.min(0.75, 1 - 360 / usableWidth))
    : 0.75;
  return Math.min(max, Math.max(0.4, value));
}

export function readSplitRatio(saved: string | null): number {
  if (!saved) return 0.6;
  const parsed = Number(saved);
  return Number.isFinite(parsed) ? clampSplitRatio(parsed) : 0.6;
}

export function readPreference(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writePreference(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* UI remains usable without storage. */
  }
}
