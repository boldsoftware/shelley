export {};

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

const originalDateTimeFormat = Intl.DateTimeFormat;
let formatterConstructions = 0;
const countingDateTimeFormat = function (
  locales?: Intl.LocalesArgument,
  options?: Intl.DateTimeFormatOptions,
) {
  formatterConstructions++;
  return new originalDateTimeFormat(locales, options);
} as unknown as typeof Intl.DateTimeFormat;
Object.defineProperty(countingDateTimeFormat, "supportedLocalesOf", {
  value: originalDateTimeFormat.supportedLocalesOf.bind(originalDateTimeFormat),
});
Object.defineProperty(countingDateTimeFormat, "prototype", {
  value: originalDateTimeFormat.prototype,
});
Object.defineProperty(Intl, "DateTimeFormat", {
  configurable: true,
  writable: true,
  value: countingDateTimeFormat,
});

try {
  const { formatAbsolute, formatDay, formatTime } = await import("./messageTime");
  const date = new Date("2026-08-04T12:34:56Z");
  const sameYear = new Date("2026-01-01T00:00:00");
  const otherYear = new Date("2025-01-01T00:00:00Z");
  const expectedTime = new originalDateTimeFormat([], {
    hour: "numeric",
    minute: "2-digit",
  }).format(date);
  const expectedAbsolute = new originalDateTimeFormat([], {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  }).format(date);
  const expectedSameYearDay = new originalDateTimeFormat([], {
    weekday: "short",
    month: "short",
    day: "numeric",
  }).format(date);
  const expectedOtherYearDay = new originalDateTimeFormat([], {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: "numeric",
  }).format(date);

  assert(formatTime(date) === expectedTime, "formatTime preserves locale output");
  assert(formatAbsolute(date) === expectedAbsolute, "formatAbsolute preserves locale output");
  assert(formatDay(date, sameYear) === expectedSameYearDay, "formatDay omits the current year");
  assert(formatDay(date, otherYear) === expectedOtherYearDay, "formatDay includes another year");
  assert(formatterConstructions === 4, "formatters are constructed once at module load");

  for (let i = 0; i < 20; i++) {
    formatTime(date);
    formatAbsolute(date);
    formatDay(date, sameYear);
    formatDay(date, otherYear);
  }
  assert(formatterConstructions === 4, "formatting reuses cached Intl instances");
} finally {
  Object.defineProperty(Intl, "DateTimeFormat", {
    configurable: true,
    writable: true,
    value: originalDateTimeFormat,
  });
}

console.log(`messageTime: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
