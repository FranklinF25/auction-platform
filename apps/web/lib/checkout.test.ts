import { describe, expect, it } from "vitest";
import {
  DECLINE_CARD_FORMATTED,
  DECLINE_CARD_NUMBER,
  cardDigits,
  formatCardNumber,
  isValidCardNumber,
} from "@/lib/checkout";

describe("cardDigits", () => {
  it("strips spaces and dashes but keeps digits", () => {
    expect(cardDigits("4242 4242 4242 4242")).toBe("4242424242424242");
    expect(cardDigits("4242-4242-4242-4242")).toBe("4242424242424242");
    expect(cardDigits(" 4000 0000 0000 0002 ")).toBe(DECLINE_CARD_NUMBER);
  });
});

describe("isValidCardNumber", () => {
  it("accepts 12 to 19 digits", () => {
    expect(isValidCardNumber("424242424242")).toBe(true); // 12, minimum
    expect(isValidCardNumber("4242424242424242")).toBe(true); // 16, typical
    expect(isValidCardNumber("4242424242424242424")).toBe(true); // 19, maximum
  });

  it("accepts digits regardless of spacing", () => {
    expect(isValidCardNumber(DECLINE_CARD_FORMATTED)).toBe(true);
    expect(isValidCardNumber("4242-4242-4242-4242")).toBe(true);
  });

  it("rejects too-short, too-long, and non-numeric input", () => {
    expect(isValidCardNumber("42424242424")).toBe(false); // 11 digits
    expect(isValidCardNumber("42424242424242424242")).toBe(false); // 20 digits
    expect(isValidCardNumber("4242abcd4242")).toBe(false);
    expect(isValidCardNumber("")).toBe(false);
  });
});

describe("formatCardNumber", () => {
  it("groups digits with a space every 4 as they are typed", () => {
    expect(formatCardNumber("4")).toBe("4");
    expect(formatCardNumber("4242")).toBe("4242");
    expect(formatCardNumber("42424")).toBe("4242 4");
    expect(formatCardNumber("4242424242424242")).toBe("4242 4242 4242 4242");
    expect(formatCardNumber("424242424242424")).toBe("4242 4242 4242 424");
  });

  it("never leaves a trailing space on a 4-digit boundary", () => {
    expect(formatCardNumber("4242")).not.toMatch(/ $/);
    expect(formatCardNumber("42424242")).not.toMatch(/ $/);
  });

  it("drops non-digits and caps the input at 19 digits", () => {
    expect(formatCardNumber("42 42-42ab42")).toBe("4242 4242");
    expect(formatCardNumber("1".repeat(23))).toBe("1111 1111 1111 1111 111");
  });

  it("round-trips already-formatted input through cardDigits", () => {
    expect(cardDigits(formatCardNumber(DECLINE_CARD_NUMBER))).toBe(
      DECLINE_CARD_NUMBER,
    );
  });
});
