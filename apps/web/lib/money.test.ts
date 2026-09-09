import { describe, expect, it } from "vitest";
import { formatUSD, parseDollarsToCents } from "@/lib/money";

describe("formatUSD", () => {
  it("formats plain cent amounts", () => {
    expect(formatUSD(0)).toBe("$0.00");
    expect(formatUSD(5)).toBe("$0.05");
    expect(formatUSD(100)).toBe("$1.00");
    expect(formatUSD(199)).toBe("$1.99");
    expect(formatUSD(123456)).toBe("$1,234.56");
  });

  it("formats large amounts with thousands separators", () => {
    expect(formatUSD(123456789)).toBe("$1,234,567.89");
  });

  it("formats negative amounts", () => {
    expect(formatUSD(-500)).toBe("-$5.00");
    expect(formatUSD(-99)).toBe("-$0.99");
  });
});

describe("parseDollarsToCents", () => {
  it("parses simple dollar amounts", () => {
    expect(parseDollarsToCents("1.00")).toBe(100);
    expect(parseDollarsToCents("0.99")).toBe(99);
    expect(parseDollarsToCents("5")).toBe(500);
    expect(parseDollarsToCents("0")).toBe(0);
  });

  it("pads a single decimal digit", () => {
    expect(parseDollarsToCents("19.9")).toBe(1990);
    expect(parseDollarsToCents("0.1")).toBe(10);
  });

  it("accepts thousands separators, with or without a currency sign", () => {
    expect(parseDollarsToCents("1,234.56")).toBe(123456);
    expect(parseDollarsToCents("1234.56")).toBe(123456);
    expect(parseDollarsToCents("$3.00")).toBe(300);
    expect(parseDollarsToCents("$12,345.67")).toBe(1234567);
  });

  it("parses negatives so the caller can enforce business rules", () => {
    expect(parseDollarsToCents("-2.50")).toBe(-250);
    expect(parseDollarsToCents("-0.01")).toBe(-1);
  });

  it("trims surrounding whitespace", () => {
    expect(parseDollarsToCents("  12.34  ")).toBe(1234);
    expect(parseDollarsToCents("\t9.99\n")).toBe(999);
  });

  it("rejects malformed input with null", () => {
    expect(parseDollarsToCents("")).toBeNull();
    expect(parseDollarsToCents("   ")).toBeNull();
    expect(parseDollarsToCents("abc")).toBeNull();
    expect(parseDollarsToCents("1.234")).toBeNull(); // three decimals
    expect(parseDollarsToCents("1.")).toBeNull(); // trailing dot
    expect(parseDollarsToCents(".50")).toBeNull(); // missing whole part
    expect(parseDollarsToCents("1,23,456")).toBeNull(); // broken grouping
    expect(parseDollarsToCents("12,34.56")).toBeNull();
    expect(parseDollarsToCents("1 234.56")).toBeNull(); // embedded space
    expect(parseDollarsToCents("5 dollars")).toBeNull();
  });
});
