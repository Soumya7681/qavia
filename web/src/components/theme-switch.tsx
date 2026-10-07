"use client";

import { MonitorIcon, MoonIcon, SunIcon } from "lucide-react";
import { useTheme } from "next-themes";
import { useSyncExternalStore } from "react";

import { Button } from "@/components/ui/button";

const options = [
  { value: "light", label: "Light", icon: SunIcon },
  { value: "dark", label: "Dark", icon: MoonIcon },
  { value: "system", label: "System", icon: MonitorIcon },
] as const;

const subscribe = () => () => {};

// The chosen theme lives in the browser, so the server cannot know which option is
// selected. Until hydration finishes, nothing is shown as selected; marking one on
// the server would be a guess, and a wrong guess is a hydration mismatch.
export function ThemeSwitch() {
  const { theme, setTheme } = useTheme();
  const hydrated = useSyncExternalStore(
    subscribe,
    () => true,
    () => false,
  );

  return (
    <div role="group" aria-label="Theme" className="flex gap-1">
      {options.map(({ value, label, icon: Icon }) => {
        const selected = hydrated && theme === value;
        return (
          <Button
            key={value}
            size="sm"
            variant={selected ? "secondary" : "ghost"}
            aria-pressed={selected}
            onClick={() => setTheme(value)}
          >
            <Icon data-icon="inline-start" aria-hidden />
            {label}
          </Button>
        );
      })}
    </div>
  );
}
