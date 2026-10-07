"use client";

import { useTheme } from "next-themes";
import { createContext, useContext, useEffect } from "react";

import type { Preferences } from "@/lib/auth/server";

const PreferencesContext = createContext<Preferences | null>(null);

/**
 * The user's preferences from the settings registry. The stored theme wins over
 * whatever this browser last used, so a theme chosen on one machine follows the
 * user to the next.
 */
export function PreferencesProvider({
  preferences,
  children,
}: {
  preferences: Preferences;
  children: React.ReactNode;
}) {
  const { theme, setTheme } = useTheme();
  useEffect(() => {
    if (theme !== preferences.theme) setTheme(preferences.theme);
    // Only when the stored preference changes, not when the user toggles locally.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preferences.theme]);

  return <PreferencesContext.Provider value={preferences}>{children}</PreferencesContext.Provider>;
}

export function usePreferences(): Preferences | null {
  return useContext(PreferencesContext);
}
