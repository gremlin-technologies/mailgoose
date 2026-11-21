// Package mail sends fully built email messages through a provider.
//
// Everything that sends mail (sign-in codes, double opt-in confirmations,
// test sends, campaigns) builds a Message and hands it to a Sender. Which
// Sender runs is a setting: SES in AWS, and Fake in tests and local
// development, which records messages and never calls AWS.
package mail

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

// Kind says what a message is for. It travels with the message as an SES tag,
// so SES events can be routed without a database lookup. The zero value is no
// kind: Validate refuses it, and Guard refuses it from the system domain, so
// every message must set one.
type Kind string

// Kinds of message.
const (
	KindSystem   Kind = "system"   // sign-in codes and other mail from mailgoose itself
	KindConfirm  Kind = "confirm"  // double opt-in confirmations
	KindTest     Kind = "test"     // test sends
	KindCampaign Kind = "campaign" // campaign sends
)

// Address is a mailbox with an optional display name.
type Address struct {
	Name  string
	Email string
}

// String formats the address for a header, encoding a non-ASCII name as
// RFC 2047 requires.
func (a Address) String() string {
	return (&mail.Address{Name: a.Name, Address: a.Email}).String()
}

// Header is one extra message header, such as List-Unsubscribe.
type Header struct {
	Name  string
	Value string
}

// Message is a fully built email to one recipient. Rendering, merge tags and
// unsubscribe links are done before it gets here.
type Message struct {
	From    Address
	To      Address
	ReplyTo []Address
	Subject string
	HTML    string
	Text    string
	// Headers are added as they are, in order.
	Headers []Header

	Kind       Kind
	AccountID  string // empty for mailgoose's own mail
	CampaignID string // set for campaign sends
}

// ErrRetryLater marks a send the provider refused for now, not for good:
// it's throttling, the account's sending rate or daily quota is used up, or
// sending is paused on the provider's side (MG-87). The same message can be
// sent later. errors.Is finds it; the provider's own error is still wrapped
// and gives the error its text.
var ErrRetryLater = errors.New("the mail provider asked to retry later")

// retryLater is a provider error marked ErrRetryLater.
type retryLater struct{ err error }

func (e *retryLater) Error() string   { return e.err.Error() }
func (e *retryLater) Unwrap() []error { return []error{ErrRetryLater, e.err} }

// Sender sends a message and returns the provider's message ID.
type Sender interface {
	Send(ctx context.Context, m Message) (messageID string, err error)
}

// reservedHeaders are set by the sender from Message fields; a caller can't
// override them through Headers.
var reservedHeaders = map[string]bool{
	"from": true, "to": true, "cc": true, "bcc": true, "reply-to": true,
	"subject": true, "content-type": true, "content-transfer-encoding": true,
	"mime-version": true, "message-id": true, "date": true, "return-path": true,
	"sender": true,
}

// Validate reports what's wrong with m, so every Sender rejects the same
// messages.
func (m Message) Validate() error {
	var errs []error
	if _, err := mail.ParseAddress(m.From.Email); err != nil || m.From.Email == "" {
		errs = append(errs, fmt.Errorf("from address %q is not valid", m.From.Email))
	}
	if _, err := mail.ParseAddress(m.To.Email); err != nil || m.To.Email == "" {
		errs = append(errs, fmt.Errorf("to address %q is not valid", m.To.Email))
	}
	for _, r := range m.ReplyTo {
		if _, err := mail.ParseAddress(r.Email); err != nil {
			errs = append(errs, fmt.Errorf("reply-to address %q is not valid", r.Email))
		}
	}
	if strings.TrimSpace(m.Subject) == "" {
		errs = append(errs, errors.New("subject is empty"))
	}
	if m.HTML == "" && m.Text == "" {
		errs = append(errs, errors.New("message has no body"))
	}
	for _, h := range m.Headers {
		if !validHeaderName(h.Name) {
			errs = append(errs, fmt.Errorf("header name %q is not valid", h.Name))
		} else if reservedHeaders[strings.ToLower(h.Name)] {
			errs = append(errs, fmt.Errorf("header %s is set from the message fields", h.Name))
		}
		if strings.ContainsAny(h.Value, "\r\n") {
			errs = append(errs, fmt.Errorf("header %s has a line break", h.Name))
		}
	}
	for _, a := range append([]Address{m.From, m.To}, m.ReplyTo...) {
		if strings.ContainsAny(a.Name, "\r\n") {
			errs = append(errs, fmt.Errorf("display name for %s has a line break", a.Email))
		}
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		errs = append(errs, errors.New("subject has a line break"))
	}
	switch m.Kind {
	case KindSystem, KindConfirm, KindTest, KindCampaign:
	default:
		errs = append(errs, fmt.Errorf("kind %q is not one of system, confirm, test, campaign", m.Kind))
	}
	return errors.Join(errs...)
}

func validHeaderName(n string) bool {
	if n == "" {
		return false
	}
	for _, c := range n {
		if c <= ' ' || c > '~' || c == ':' {
			return false
		}
	}
	return true
}
