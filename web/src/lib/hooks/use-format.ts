"use client";

import { useContext, useMemo } from "react";

import { CurrentUserContext } from "@/components/auth/current-user";
import * as format from "@/lib/format";

/** The formatting helpers bound to the signed-in user's timezone. */
export function useFormat() {
  const user = useContext(CurrentUserContext);
  const timeZone = user?.timezone;
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
