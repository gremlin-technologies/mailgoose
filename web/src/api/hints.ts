// The web app's own words for the API's errors (MG-121). The API's hints are
// written for the CLI and agents, so most of them name a `mailgoose ...`
// command or a --flag; the web app points to the page or control that fixes
// each problem instead. Everything the web app shows from the API goes
// through webWords, so a new API hint can't leak a CLI command into the UI:
// a hint with no wording here is dropped if it names one.

/** Matches text that names a CLI command, a flag or a raw API route. */
const CLI = /`mailgoose\b|(^|[\s`(])--[a-z]|\b(?:POST|GET|PUT|PATCH|DELETE) \/v1\//;

/** True when text names a CLI command, a flag or an API route. */
export function namesCli(text: string): boolean {
  return CLI.test(text);
}

/** Drops each sentence of text that names a CLI command; what's left reads on. */
export function withoutCli(text: string): string {
  if (!namesCli(text)) return text;
  return text
    .split(/(?<=[.!?])\s+/)
    .filter((s) => !namesCli(s))
    .join(" ")
    .trim();
}

/**
 * The web app's wording for an error code's hint, in terms of pages and
 * controls. An empty string means the message says enough and the hint is
 * left out. Codes not listed keep the API's hint unless it names the CLI.
 */
export const WEB_HINTS: Record<string, string> = {
  // What stops a test or a launch.
  check_failed: "Open the email, fix what the check lists, then try again.",
  send_at_passed: "Choose a new send time for the campaign, or send it now.",
  subscription_past_due: "Update your card on the Billing page. Sending resumes as soon as the payment goes through.",
  no_recipients: "Choose who the campaign goes to: everyone, a tag or a segment.",
  credits_short: "Buy credits on the Billing page, or send to fewer contacts.",
  segment_invalid: "Fix the segment's conditions on its page, or pick other recipients for the campaign.",
  test_mode:
    "Send to a tag or segment that holds only your confirmed test addresses (see Test addresses), or ask mailgoose to allow full sending.",
  tester_limit: "",
  over_contact_limit: "Upgrade to the paid plan on the Billing page, or delete or unsubscribe contacts you no longer mail.",
  new_terms_not_accepted: "Read what changed on the Policies page, then accept the new terms.",
  no_email: "Pick an email for the campaign.",
  missing_from: "Set a From address in Settings, or on the email.",
  unverified_reply_to:
    "Use a Reply-To on a verified domain or one of your sender addresses, or clear it so replies go to the From address.",
  missing_postal_address: "Add your postal address in Settings.",
  missing_subject: "Add a subject to the email.",
  empty_email: "Add the email's markup in the editor.",
  invalid_html: "Fix the markup in the editor and save it again.",
  merge_tag: "Run the email check to see which merge tags or links to fix.",
  invalid_merge_tags: "Fix them in the editor, where each one is marked.",

  // Signing in.
  invalid_code: "Send a new code and use the one in the newest email.",

  // Plan and billing.
  plan_required: "Upgrade to the paid plan on the Billing page.",
  already_subscribed: "Change or cancel it from the Billing page.",
  not_subscribed: "Start the paid plan on the Billing page.",

  // Domains, sender addresses and test addresses.
  domain_exists: "Finish verifying it on the Domains page.",
  no_verified_from: "Verify a domain on the Domains page, then set your From address in Settings.",
  sender_address_exists: "Resend its confirmation email from the Sender addresses page.",
  sender_address_limit: "Remove one on the Sender addresses page to add another.",
  sender_address_email_failed: "Resend its confirmation email from the Sender addresses page.",
  sender_address_link_expired: "Resend its confirmation email from the Sender addresses page.",
  test_address_exists: "Resend its confirmation email from the Test addresses page.",
  test_address_limit: "Remove one on the Test addresses page to add another.",
  test_address_email_failed: "Resend its confirmation email from the Test addresses page.",
  test_address_link_expired: "Resend its confirmation email from the Test addresses page.",
  test_recipient_not_allowed: "Send tests to your own address, or to an address on a verified domain.",

  // Contacts, tags, segments, images, emails and forms.
  tag_exists: "Pick the existing tag instead.",
  unknown_tag: "Pick one of your tags.",
  unknown_email: "Pick one of your emails.",
  unknown_segment: "Pick one of your segments.",
  unknown_campaign: "Pick one of your campaigns.",
  contact_exists: "Find them on the Contacts page.",
  segment_exists: "Pick a different name, or open the segment with that name.",
  segment_limit: "Delete a segment you no longer use first.",
  too_many_images: "Delete images you no longer use first.",
  draft: "Check its fields, tags and messages, then make it live on its page.",
  form_not_ready: "Verify a sending domain on the Domains page, so the form can send confirmation emails.",

  // Imports.
  import_not_waiting: "",
  mailchimp_not_configured: "Upload Mailchimp's export file instead.",
  mailchimp_not_connected: "Connect your Mailchimp account on the Mailchimp import page.",
  mailchimp_access_revoked: "Connect your Mailchimp account again on the Mailchimp import page.",
  invalid_audience_ids: "Pick audiences from the list shown.",
  fetch_in_progress: "Wait for the fetch to finish, then try again.",
  too_many_uploads: "Delete an earlier upload on the Mailchimp import page first.",

  // Reports and campaigns.
  report_archived: "",
  tracking_off: "",
};

/** The message the web app shows for a code, where the API's message says it differently. */
export const WEB_MESSAGES: Record<string, string> = {
  untested: "Send a test and check it looks right before you launch.",
};

/** Codes whose hint says how long to wait, worked out from Retry-After (MG-123). */
export const WAIT_CODES = new Set(["rate_limited", "too_many_wrong_codes"]);

/**
 * How long to wait before trying again, from a Retry-After header in seconds,
 * in whole minutes rounded up: never seconds, never the header's name. With
 * no usable header it says an hour, the longest any of these limits lasts.
 */
export function waitHint(retryAfter: string | null): string {
  const secs = Number(retryAfter);
  if (retryAfter === null || retryAfter.trim() === "" || !Number.isFinite(secs) || secs <= 0) {
    return "Try again in an hour.";
  }
  const minutes = Math.ceil(secs / 60);
  return minutes === 1 ? "Try again in a minute." : `Try again in ${minutes} minutes.`;
}

/** What the web app shows for an API error's message and hint. */
export function webWords(code: string, message: string, hint: string): { message: string; hint: string } {
  const own = WEB_MESSAGES[code];
  if (own !== undefined) return { message: own, hint: "" };
  const words = code in WEB_HINTS ? WEB_HINTS[code] : withoutCli(hint);
  return { message: withoutCli(message) || "That didn't work.", hint: words };
}
