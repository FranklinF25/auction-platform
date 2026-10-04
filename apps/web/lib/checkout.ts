// Pure checkout helpers (M4): demo-card input formatting and client-side
// validation mirroring the API's card rule — 12 to 19 digits after stripping
// spaces and dashes. The decline card constant is single-sourced here so the
// UI documents exactly what the backend declines.

/** The demo decline card: paying with exactly this number fails the payment
 * (status "failed", retryable) so the decline flow can be exercised. */
export const DECLINE_CARD_NUMBER = "4000000000000002";

/** The decline card as shown to users in the checkout helper text. */
export const DECLINE_CARD_FORMATTED = "4000 0000 0000 0002";

/** Strips the separators the API tolerates (spaces and dashes), leaving the
 * raw digit string the backend validates. */
export function cardDigits(input: string): string {
  return input.replace(/[\s-]/g, "");
}

/** True when the input is a payable card number: 12–19 digits after stripping
 * spaces and dashes (the server's validation_error rule, mirrored client-side
 * so obvious typos never leave the form). */
export function isValidCardNumber(input: string): boolean {
  return /^\d{12,19}$/.test(cardDigits(input));
}

/** Formats typed card input for display: keeps only digits (capped at 19, the
 * longest card the demo accepts) and groups them with a space every 4 digits —
 * "424242424242424" -> "4242 4242 4242 424", never a trailing space. */
export function formatCardNumber(input: string): string {
  const digits = input.replace(/\D/g, "").slice(0, 19);
  return digits.replace(/(\d{4})(?=\d)/g, "$1 ");
}
