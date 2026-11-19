package campaigns

// Campaign statuses (CAM-9).
const (
	// StatusDraft is a campaign being set up. It can be edited and deleted.
	StatusDraft = "draft"
	// StatusScheduled is a launched campaign waiting for its send time. It
	// can be unscheduled back to a draft, or cancelled.
	StatusScheduled = "scheduled"
	// StatusSending is a campaign going out.
	StatusSending = "sending"
	// StatusSent is a campaign that finished.
	StatusSent = "sent"
	// StatusCancelled is a campaign stopped by the account before or while
	// sending.
	StatusCancelled = "cancelled"
	// StatusPaused is a campaign stopped by us mid-send (CMP-6, CMP-8).
	StatusPaused = "paused"
	// StatusFailed is a campaign that couldn't finish.
	StatusFailed = "failed"
)

// Statuses lists every status, in the order a campaign usually moves.
var Statuses = []string{StatusDraft, StatusScheduled, StatusSending, StatusSent, StatusCancelled, StatusPaused, StatusFailed}

// transitions lists where each status can go. Sent, cancelled and failed
// are the end.
var transitions = map[string][]string{
	// Launch (MG-30): scheduled for later, or sending now.
	StatusDraft: {StatusScheduled, StatusSending},
	// The scheduler starts it, or the account unschedules or cancels it
	// (MG-32).
	StatusScheduled: {StatusSending, StatusDraft, StatusCancelled},
	// It finishes, we pause it, it fails, or the account stops it.
	StatusSending: {StatusSent, StatusPaused, StatusFailed, StatusCancelled},
	// We resume it, or it's stopped for good.
	StatusPaused: {StatusSending, StatusCancelled, StatusFailed},
}

// CanMove reports whether a campaign can go from one status to another.
func CanMove(from, to string) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Editable reports whether a campaign in this status can be changed or
// tested: only drafts can. A launched campaign was confirmed as it is; a
// scheduled one goes back to a draft to change it (MG-32).
func Editable(status string) bool {
	return status == StatusDraft
}
