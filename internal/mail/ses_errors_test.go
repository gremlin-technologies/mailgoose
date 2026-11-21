package mail

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
)

// The SES errors that mean "not now" become ErrRetryLater (MG-87); SES's
// other client errors, a refusal of the message, become ErrRejected
// (MG-101); the rest, which may hide a message that went, are left as they
// are.
func TestClassifySESError(t *testing.T) {
	msg := func(s string) *string { return aws.String(s) }
	for name, tc := range map[string]struct {
		err      error
		retry    bool
		rejected bool
	}{
		"too many requests":   {&types.TooManyRequestsException{Message: msg("Too many requests")}, true, false},
		"limit exceeded":      {&types.LimitExceededException{Message: msg("Limit exceeded")}, true, false},
		"sending paused":      {&types.SendingPausedException{Message: msg("Sending is paused for this account")}, true, false},
		"account suspended":   {&types.AccountSuspendedException{Message: msg("The account is suspended")}, true, false},
		"daily quota":         {&types.MessageRejected{Message: msg("Daily message quota exceeded.")}, true, false},
		"sending rate":        {&types.MessageRejected{Message: msg("Maximum sending rate exceeded.")}, true, false},
		"quota any case":      {&types.MessageRejected{Message: msg("DAILY MESSAGE QUOTA EXCEEDED")}, true, false},
		"generic throttling":  {&smithy.GenericAPIError{Code: "Throttling", Message: "Rate exceeded"}, true, false},
		"generic rejected":    {&smithy.GenericAPIError{Code: "MessageRejected", Message: "Maximum sending rate exceeded."}, true, false},
		"other rejection":     {&types.MessageRejected{Message: msg("Email address is not verified.")}, false, true},
		"not verified domain": {&types.MailFromDomainNotVerifiedException{Message: msg("not verified")}, false, true},
		"bad request":         {&types.BadRequestException{Message: msg("Illegal address")}, false, true},
		"not found":           {&types.NotFoundException{Message: msg("no configuration set")}, false, true},
		"plain":               {errors.New("connection reset"), false, false},
		"server fault":        {&smithy.GenericAPIError{Code: "InternalFailure", Message: "oops", Fault: smithy.FaultServer}, false, false},
		"unknown fault":       {&smithy.GenericAPIError{Code: "Mystery", Message: "?"}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			wrapped := fmt.Errorf("ses: send: %w", tc.err)
			got := classifySESError(wrapped)
			if errors.Is(got, ErrRetryLater) != tc.retry {
				t.Fatalf("retry later = %v, want %v (%v)", !tc.retry, tc.retry, got)
			}
			if errors.Is(got, ErrRejected) != tc.rejected || NotSent(got) != (tc.retry || tc.rejected) {
				t.Fatalf("rejected = %v, want %v; not sent = %v (%v)", !tc.rejected, tc.rejected, NotSent(got), got)
			}
			// Either way the provider's error is still there, and the text
			// is what it was.
			if !errors.Is(got, tc.err) || got.Error() != wrapped.Error() {
				t.Errorf("got %q, lost %v", got, tc.err)
			}
		})
	}
	if classifySESError(nil) != nil {
		t.Error("nil became an error")
	}
}

func TestSESSendRetryLater(t *testing.T) {
	ses := SES{Client: &fakeSES{err: &types.TooManyRequestsException{Message: aws.String("slow down")}}, ConfigurationSet: "cs"}
	if _, err := ses.Send(context.Background(), sample()); !errors.Is(err, ErrRetryLater) {
		t.Fatalf("throttled send: %v", err)
	}
	ses.Client = &fakeSES{err: &types.MessageRejected{Message: aws.String("Email address is not verified.")}}
	if _, err := ses.Send(context.Background(), sample()); err == nil || errors.Is(err, ErrRetryLater) || !errors.Is(err, ErrRejected) {
		t.Fatalf("rejected send: %v", err)
	}
	// An invalid message never reaches SES, so it wasn't sent either.
	bad := sample()
	bad.To = Address{}
	if _, err := ses.Send(context.Background(), bad); !errors.Is(err, ErrRejected) || !NotSent(err) {
		t.Fatalf("invalid message: %v", err)
	}
}
