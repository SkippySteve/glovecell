import {useEffect, useState} from 'react';

// Current time, refreshed every second while mounted, for countdowns.
export function useNow(intervalMs = 1000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => { const t = window.setInterval(() => setNow(Date.now()), intervalMs); return () => window.clearInterval(t); }, [intervalMs]);
  return now;
}

// "12:04" under an hour, "1 h 05 min" under a day, "6 d 3 h" beyond.
export function left(until: number, now: number): string {
  const s = Math.max(0, Math.round((until - now) / 1000));
  if (s >= 86400) return `${Math.floor(s / 86400)} d ${Math.floor(s % 86400 / 3600)} h`;
  if (s >= 3600) return `${Math.floor(s / 3600)} h ${String(Math.floor(s % 3600 / 60)).padStart(2, '0')} min`;
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

export const clockAt = (ms: number) => new Date(ms).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
export const stamp = (ms: number) => ms ? new Date(ms).toLocaleString([], {month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit'}) : '';
export function ago(ms: number, now: number): string {
  const s = (now - ms) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return Math.round(s / 60) + ' min ago';
  if (s < 86400) return Math.round(s / 3600) + ' h ago';
  return Math.round(s / 86400) + ' d ago';
}
// Share of a window still left, 0–100, for progress bars.
export const remaining = (until: number, total: number, now: number) => total > 0 ? Math.max(0, Math.min(100, (until - now) / total * 100)) : 0;
