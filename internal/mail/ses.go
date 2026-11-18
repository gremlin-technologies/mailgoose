package mail

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// SESAPI is the part of the SES v2 client the adapter uses.
type SESAPI interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, opts ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// SES sends through Amazon SES v2. Every message goes through the
// environment's configuration set, so delivery, bounce and complaint events
// reach the events queue, and carries tags naming its kind, account and
// campaign, which come back on those events.
type SES struct {
	Client           SESAPI
	ConfigurationSet string
}

// SES message tag names. The events worker (MG-33) reads them back.
const (
	TagKind     = "mg_kind"
	TagAccount  = "mg_account"
	TagCampaign = "mg_campaign"
)

// SES tag values may only hold ASCII letters, digits, '_' and '-'.
var unsafeTagChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func tagValue(v string) string { return unsafeTagChars.ReplaceAllString(v, "_") }

// Send implements Sender.
func (s SES) Send(ctx context.Context, m Message) (string, error) {
	if s.ConfigurationSet == "" {
		return "", errors.New("ses: no configuration set; set SES_CONFIGURATION_SET")
	}
	if err := m.Validate(); err != nil {
		return "", fmt.Errorf("ses: invalid message: %w", err)
	}

	body := &types.Body{}
	if m.HTML != "" {
		body.Html = &types.Content{Data: aws.String(m.HTML), Charset: aws.String("UTF-8")}
	}
	if m.Text != "" {
		body.Text = &types.Content{Data: aws.String(m.Text), Charset: aws.String("UTF-8")}
	}
	headers := make([]types.MessageHeader, 0, len(m.Headers))
	for _, h := range m.Headers {
		headers = append(headers, types.MessageHeader{Name: aws.String(h.Name), Value: aws.String(h.Value)})
	}
	replyTo := make([]string, 0, len(m.ReplyTo))
	for _, r := range m.ReplyTo {
		replyTo = append(replyTo, r.String())
	}
	tags := []types.MessageTag{{Name: aws.String(TagKind), Value: aws.String(tagValue(string(m.Kind)))}}
	if m.AccountID != "" {
		tags = append(tags, types.MessageTag{Name: aws.String(TagAccount), Value: aws.String(tagValue(m.AccountID))})
	}
	if m.CampaignID != "" {
		tags = append(tags, types.MessageTag{Name: aws.String(TagCampaign), Value: aws.String(tagValue(m.CampaignID))})
	}

	out, err := s.Client.SendEmail(ctx, &sesv2.SendEmailInput{
		ConfigurationSetName: aws.String(s.ConfigurationSet),
		FromEmailAddress:     aws.String(m.From.String()),
		Destination:          &types.Destination{ToAddresses: []string{m.To.String()}},
		ReplyToAddresses:     replyTo,
		EmailTags:            tags,
		Content: &types.EmailContent{Simple: &types.Message{
			// SES wants a 7-bit subject; Q-encoding leaves plain ASCII as is.
			Subject: &types.Content{Data: aws.String(mime.QEncoding.Encode("utf-8", m.Subject))},
			Body:    body,
			Headers: headers,
		}},
	})
	if err != nil {
		return "", fmt.Errorf("ses: send: %w", err)
	}
	return aws.ToString(out.MessageId), nil
}
