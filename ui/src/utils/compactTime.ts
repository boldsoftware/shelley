// The sidebar's ultra-compact age of a timestamp: "now", then 5m, 3h, 2d, 1w,
// 4mo, 2y. Units come from the locale (keys below), each with an {n}.

export type CompactTimeKey =
  | "timeNow"
  | "timeMinutes"
  | "timeHours"
  | "timeDays"
  | "timeWeeks"
  | "timeMonths"
  | "timeYears";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

export function formatCompactTime(deltaMs: number, t: (key: CompactTimeKey) => string): string {
  // Clock skew can put a timestamp slightly in the future: that is "now".
  const unit = (key: CompactTimeKey, n: number) => t(key).replace("{n}", String(Math.floor(n)));
  if (deltaMs < MINUTE) return t("timeNow");
  if (deltaMs < HOUR) return unit("timeMinutes", deltaMs / MINUTE);
  if (deltaMs < DAY) return unit("timeHours", deltaMs / HOUR);
  const days = deltaMs / DAY;
  if (days < 7) return unit("timeDays", days);
  if (days < 30) return unit("timeWeeks", days / 7);
  if (days < 365) return unit("timeMonths", Math.max(1, days / 30.4375));
  return unit("timeYears", days / 365);
}
