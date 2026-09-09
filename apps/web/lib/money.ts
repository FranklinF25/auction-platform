// Money helpers. The API speaks integer cents (no floats in the money path,
// per the PRD); the UI speaks dollars. Every conversion goes through these
// two functions.

const usd = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
});

/** Formats an integer amount of cents as a USD string, e.g. 1234 -> "$12.34". */
export function formatUSD(cents: number): string {
  return usd.format(cents / 100);
}

/**
 * Parses a dollar amount typed by a user into integer cents.
 *
 * Accepts an optional leading "$" or "-", optional thousands separators, and
 * up to two decimals: "1.00" -> 100, "1,234.5" -> 123450, "-2.50" -> -250,
 * "$3.00" -> 300. Negative values parse (the caller enforces business rules
 * like "prices must be positive").
 *
 * Returns null for anything else: empty/blank input, letters, three or more
 * decimals, trailing dots, or malformed grouping ("1,23,456").
 */
export function parseDollarsToCents(input: string): number | null {
  const raw = input.trim().replace(/^\$/, "");
  const match = /^(-?)(\d{1,3}(?:,\d{3})*|\d+)(?:\.(\d{1,2}))?$/.exec(raw);
  if (!match) return null;

  const sign = match[1] === "-" ? -1 : 1;
  const whole = Number(match[2].replace(/,/g, ""));
  const fraction = match[3] ? Number(match[3].padEnd(2, "0")) : 0;
  return sign * (whole * 100 + fraction);
}
