package mail

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/gremlin-technologies/mailgoose/internal/config"
)

// New returns the Sender cfg.MailSender names. Only "ses" loads AWS
// credentials, so tests and local runs never reach AWS.
func New(ctx context.Context, cfg config.Config) (Sender, error) {
	switch cfg.MailSender {
	case "fake":
		return &Fake{Dir: cfg.MailCaptureDir}, nil
	case "ses":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
		if err != nil {
			return nil, fmt.Errorf("load AWS config: %w", err)
		}
		return SES{Client: sesv2.NewFromConfig(awsCfg), ConfigurationSet: cfg.SESConfigurationSet}, nil
	default:
		return nil, fmt.Errorf("unknown mail sender %q", cfg.MailSender)
	}
}
