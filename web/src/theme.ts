import { useSyncExternalStore } from "react";

/**
 * Light or dark. The app is light unless the person turns on dark mode in
 * Settings; the choice is kept in this browser (localStorage) and set as
 * data-theme="dark" on <html>, which switches the tokens in styles.css.
 */
export type Theme = "light" | "dark";

const key = "mailgoose.theme";
const listeners = new Set<() => void>();

function read(): Theme {
  try {
    return window.localStorage.getItem(key) === "dark" ? "dark" : "light";
  } catch {
    return "light";
  }
}

let current: Theme = read();

function apply(theme: Theme) {
  const root = document.documentElement;
  if (theme === "dark") root.dataset.theme = "dark";
  else delete root.dataset.theme;
}

function changed(theme: Theme) {
  current = theme;
  apply(theme);
  listeners.forEach((l) => l());
}

/** Applies the saved theme, and follows a change made in another tab. Call once before rendering. */
export function startTheme() {
  apply(current);
  window.addEventListener("storage", (e) => {
    if (e.key === key || e.key === null) changed(read());
  });
}

/** Turns dark mode on or off, and remembers it in this browser. */
export function setTheme(theme: Theme) {
  try {
    if (theme === "dark") window.localStorage.setItem(key, "dark");
    else window.localStorage.removeItem(key);
  } catch {
    // Private windows can refuse storage; the choice still holds until reload.
  }
  changed(theme);
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** The current theme, re-rendering when it changes. */
export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, () => current, () => "light");
}
