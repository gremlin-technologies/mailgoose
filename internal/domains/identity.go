package domains

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// TagAccount is the SES identity tag naming the mailgoose account it belongs to.
const TagAccount = "mg_account"

// ParseTags reads "Key=Value[,Key=Value]" (SES_IDENTITY_TAG) into a map.
func ParseTags(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("SES_IDENTITY_TAG: %q is not Key=Value", part)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// createIdentity makes the SES identity for name with Easy DKIM and the
// MAIL FROM subdomain, and returns its three DKIM tokens. An identity SES
// already has is adopted if its tag names this account; otherwise it is
// ErrUnavailable, unless replace says this account just took the name over.
func (s *Service) createIdentity(ctx context.Context, accountID, name string, replace bool) ([]string, error) {
	if replace {
		if err := s.deleteIdentity(ctx, name); err != nil {
			return nil, err
		}
	}
	tags := []types.Tag{{Key: aws.String(TagAccount), Value: aws.String(accountID)}}
	keys := make([]string, 0, len(s.IdentityTags))
	for k := range s.IdentityTags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tags = append(tags, types.Tag{Key: aws.String(k), Value: aws.String(s.IdentityTags[k])})
	}
	in := &sesv2.CreateEmailIdentityInput{EmailIdentity: aws.String(name), Tags: tags}
	if s.ConfigurationSet != "" {
		in.ConfigurationSetName = aws.String(s.ConfigurationSet)
	}

	var tokens []string
	out, err := s.SES.CreateEmailIdentity(ctx, in)
	var exists *types.AlreadyExistsException
	switch {
	case errors.As(err, &exists):
		got, gerr := s.SES.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(name)})
		if gerr != nil || tagValue(got.Tags, TagAccount) != accountID {
			return nil, ErrUnavailable
		}
		if got.DkimAttributes != nil {
			tokens = got.DkimAttributes.Tokens
		}
	case err != nil:
		return nil, fmt.Errorf("create SES identity: %w", err)
	case out.DkimAttributes != nil:
		tokens = out.DkimAttributes.Tokens
	}
	if len(tokens) == 0 {
		return nil, errors.New("create SES identity: SES returned no DKIM tokens")
	}

	if _, err := s.SES.PutEmailIdentityMailFromAttributes(ctx, &sesv2.PutEmailIdentityMailFromAttributesInput{
		EmailIdentity:       aws.String(name),
		MailFromDomain:      aws.String(MailFromPrefix + "." + name),
		BehaviorOnMxFailure: types.BehaviorOnMxFailureUseDefaultValue,
	}); err != nil {
		return nil, fmt.Errorf("set MAIL FROM domain: %w", err)
	}
	return tokens, nil
}

func (s *Service) deleteIdentity(ctx context.Context, name string) error {
	_, err := s.SES.DeleteEmailIdentity(ctx, &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(name)})
	var nf *types.NotFoundException
	if err != nil && !errors.As(err, &nf) {
		return fmt.Errorf("delete SES identity: %w", err)
	}
	return nil
}

// sesStatus returns SES's DKIM status for name in lower case, and whether
// SES will send from it.
func (s *Service) sesStatus(ctx context.Context, name string) (string, bool) {
	out, err := s.SES.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(name)})
	var nf *types.NotFoundException
	switch {
	case errors.As(err, &nf):
		return "not_found", false
	case err != nil:
		s.log().WarnContext(ctx, "reading SES identity failed", "domain", name, "err", err)
		return "unknown", false
	}
	status := "not_started"
	if out.DkimAttributes != nil && out.DkimAttributes.Status != "" {
		status = strings.ToLower(string(out.DkimAttributes.Status))
	}
	return status, out.VerifiedForSendingStatus
}

func tagValue(tags []types.Tag, key string) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == key {
			return aws.ToString(t.Value)
		}
	}
	return ""
}
