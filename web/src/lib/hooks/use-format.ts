"use client";

import { useContext, useMemo } from "react";

import { CurrentUserContext } from "@/components/auth/current-user";
import { usePreferences } from "@/components/preferences";
import * as format from "@/lib/format";

/**
 * The formatting helpers bound to the signed-in user's timezone: the
 * `preferences.timezone` setting, falling back to the zone on their profile.
 */
export function useFormat() {
  const user = useContext(CurrentUserContext);
  const preferences = usePreferences();
  const timeZone = preferences?.timezone ?? user?.timezone;
  return useMemo(() => {
    const ctx = { timeZone };
    return {
      timeZone,
      dateTime: (v: string | number | Date) => format.formatDateTime(v, ctx),
      date: (v: string | number | Date) => format.formatDate(v, ctx),
      relative: (v: string | number | Date) => format.formatRelative(v, ctx),
      duration: format.formatDuration,
      number: (v: number) => format.formatNumber(v, ctx),
      percent: (v: number) => format.formatPercent(v, ctx),
      usd: (v: number) => format.formatUSD(v, ctx),
      bytes: format.formatBytes,
    };
  }, [timeZone]);
}
