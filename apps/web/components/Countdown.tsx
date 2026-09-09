"use client";

import { useEffect, useState } from "react";

// Client-clock countdown. TODO(M2): derive a server-time offset (from the WS
// snapshot or a time endpoint) so every client counts down against the API's
// clock rather than the local browser clock, per the PRD's timer authority.
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
  className = "",
}: {
  endsAt: string;
  className?: string;
}) {
  // 250ms keeps the seconds digit from visually skipping on slow timers.
  const now = useNow(250);
  const msLeft = Date.parse(endsAt) - now;
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
