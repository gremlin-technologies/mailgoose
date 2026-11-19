import { useSession } from "../session";

/**
 * Shown across the app while a new account waits for its sign-up review
 * (MG-66): it can build emails, import contacts and send tests, but can't
 * launch a campaign or take form signups until it's approved.
 */
export function ReviewBanner() {
  const { state } = useSession();
  if (state.status !== "signed-in" || !state.session.account.in_review) return null;
  return (
    <div className="notice notice-warn review-banner" role="status">
      <p>
        We're reviewing your new account, as we do for every new sender. Meanwhile you can build emails, import
        contacts and send yourself tests; launching campaigns and taking form signups open once it's approved.
      </p>
    </div>
  );
}
