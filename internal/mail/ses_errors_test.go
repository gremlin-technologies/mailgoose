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

// The SES errors that mean "not now" become ErrRetryLater (MG-87); the
// rest are left as they are.
func TestClassifySESError(t *testing.T) {
	msg := func(s string) *string { return aws.String(s) }
	for name, tc := range map[string]struct {
		err   error
		retry bool
	}{
		"too many requests":   {&types.TooManyRequestsException{Message: msg("Too many requests")}, true},
		"limit exceeded":      {&types.LimitExceededException{Message: msg("Limit exceeded")}, true},
		"sending paused":      {&types.SendingPausedException{Message: msg("Sending is paused for this account")}, true},
		"account suspended":   {&types.AccountSuspendedException{Message: msg("The account is suspended")}, true},
		"daily quota":         {&types.MessageRejected{Message: msg("Daily message quota exceeded.")}, true},
		"sending rate":        {&types.MessageRejected{Message: msg("Maximum sending rate exceeded.")}, true},
		"quota any case":      {&types.MessageRejected{Message: msg("DAILY MESSAGE QUOTA EXCEEDED")}, true},
		"generic throttling":  {&smithy.GenericAPIError{Code: "Throttling", Message: "Rate exceeded"}, true},
		"generic rejected":    {&smithy.GenericAPIError{Code: "MessageRejected", Message: "Maximum sending rate exceeded."}, true},
		"other rejection":     {&types.MessageRejected{Message: msg("Email address is not verified.")}, false},
		"not verified domain": {&types.MailFromDomainNotVerifiedException{Message: msg("not verified")}, false},
		"bad request":         {&types.BadRequestException{Message: msg("Illegal address")}, false},
		"not found":           {&types.NotFoundException{Message: msg("no configuration set")}, false},
		"plain":               {errors.New("connection reset"), false},
	} {
		t.Run(name, func(t *testing.T) {
			wrapped := fmt.Errorf("ses: send: %w", tc.err)
			got := classifySESError(wrapped)
			if errors.Is(got, ErrRetryLater) != tc.retry {
				t.Fatalf("retry later = %v, want %v (%v)", !tc.retry, tc.retry, got)
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
	if _, err := ses.Send(context.Background(), sample()); err == nil || errors.Is(err, ErrRetryLater) {
		t.Fatalf("rejected send: %v", err)
	}
}
