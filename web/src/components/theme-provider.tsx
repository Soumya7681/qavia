"use client";

import { ThemeProvider as NextThemesProvider } from "next-themes";

// Light, dark, or the system's choice, applied as a class on <html> before paint so
// there is no flash. The user's `preferences.theme` setting drives it once the shell
// exists (FE-0.1); until a value is known, the system default applies.
export function ThemeProvider({ children }: { children: React.ReactNode }) {
  return (
    <NextThemesProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      disableTransitionOnChange
      storageKey="qavia-theme"
    >
      {children}
    </NextThemesProvider>
  );
}
