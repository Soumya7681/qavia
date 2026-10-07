"use client";

import { useEffect, useState } from "react";

/** The current time, re-read every `intervalMs` while `active`, for countdowns. */
export function useNow(active: boolean, intervalMs = 15_000): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => setNow(new Date()), intervalMs);
    return () => clearInterval(timer);
  }, [active, intervalMs]);
  return now;
}
