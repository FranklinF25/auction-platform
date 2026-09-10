"use client";

import { useEffect, useState } from "react";

// Client-clock countdown, recentered on the server clock by offsetMs (sampled
// from server_now on every WS frame) per the PRD's "Timer authority" rule: the
// countdown is a projection of ends_at, never its own source of truth.
function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

/** Formats a remaining duration as a compact human string. */
export function formatRemaining(msLeft: number): string {
  if (msLeft <= 0) return "Closed";
  const totalSeconds = Math.floor(msLeft / 1000);
  const days = Math.floor(totalSeconds / 86_400);
  const hours = Math.floor((totalSeconds % 86_400) / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  const seconds = totalSeconds % 60;
  if (days > 0) return `${days}d ${hours}h ${minutes}m`;
  if (hours > 0) return `${hours}h ${minutes}m ${seconds}s`;
  return `${minutes}m ${seconds}s`;
}

export default function Countdown({
  endsAt,
  offsetMs = 0,
  className = "",
}: {
  endsAt: string;
  /** Server-clock offset in ms; 0 keeps the raw client clock (static grids). */
  offsetMs?: number;
  className?: string;
}) {
  // 250ms keeps the seconds digit from visually skipping on slow timers.
  const now = useNow(250);
  const msLeft = Date.parse(endsAt) - (now + offsetMs);
  const done = msLeft <= 0;
  const urgent = !done && msLeft < 60_000;

  return (
    <span
      suppressHydrationWarning
      className={`font-mono text-sm tabular-nums ${
        done ? "text-zinc-400" : urgent ? "font-semibold text-rose-600" : "text-zinc-700"
      } ${className}`}
    >
      {formatRemaining(msLeft)}
    </span>
  );
}
