package auth

// Sending modes (MG-80). An account in test mode sends only to its
// confirmed test addresses and its owner's own sign-in address; one with
// full sending sends to its whole audience. Accounts made before test mode
// existed have full sending; new ones start in the mode the server's
// NEW_ACCOUNTS_TEST_MODE setting says.
const (
	SendingTest = "test"
	SendingFull = "full"
)

func (s *Service) newAccountSending() string {
	if s.NewAccountSending == SendingFull {
		return SendingFull
	}
	return SendingTest
}
