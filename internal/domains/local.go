package domains

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// FakeSES is an in-memory SES for tests and local runs. Identities are
// verified for sending as soon as they exist, unless Unverified lists them.
type FakeSES struct {
	mu         sync.Mutex
	identities map[string][]types.Tag
	// Unverified names are reported as DKIM pending and not verifiable.
	Unverified map[string]bool
	// Failed names are reported as DKIM failed.
	Failed map[string]bool
	// Err, when set, is returned by every call.
	Err error
	// Deleted lists the names deleted, in order.
	Deleted []string
}

// Tokens are a name's DKIM tokens in the fake: stable, so tests can predict them.
func (*FakeSES) Tokens(name string, generation int) []string {
	out := make([]string, 3)
	for i := range out {
		sum := sha256.Sum256([]byte(name + "|" + strconv.Itoa(i) + "|" + strconv.Itoa(generation)))
		out[i] = hex.EncodeToString(sum[:16])
	}
	return out
}

func (f *FakeSES) generation(name string) int {
	n := 0
	for _, d := range f.Deleted {
		if d == name {
			n++
		}
	}
	return n
}

// CreateEmailIdentity implements SESAPI.
func (f *FakeSES) CreateEmailIdentity(_ context.Context, in *sesv2.CreateEmailIdentityInput, _ ...func(*sesv2.Options)) (*sesv2.CreateEmailIdentityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	if f.identities == nil {
		f.identities = map[string][]types.Tag{}
	}
	name := aws.ToString(in.EmailIdentity)
	if _, ok := f.identities[name]; ok {
		return nil, &types.AlreadyExistsException{Message: aws.String("exists")}
	}
	f.identities[name] = in.Tags
	return &sesv2.CreateEmailIdentityOutput{DkimAttributes: &types.DkimAttributes{
		Tokens: f.Tokens(name, f.generation(name)), Status: types.DkimStatusPending}}, nil
}

// PutEmailIdentityMailFromAttributes implements SESAPI.
func (f *FakeSES) PutEmailIdentityMailFromAttributes(_ context.Context, in *sesv2.PutEmailIdentityMailFromAttributesInput, _ ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityMailFromAttributesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	if _, ok := f.identities[aws.ToString(in.EmailIdentity)]; !ok {
		return nil, &types.NotFoundException{Message: aws.String("no identity")}
	}
	return &sesv2.PutEmailIdentityMailFromAttributesOutput{}, nil
}

// GetEmailIdentity implements SESAPI.
func (f *FakeSES) GetEmailIdentity(_ context.Context, in *sesv2.GetEmailIdentityInput, _ ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	name := aws.ToString(in.EmailIdentity)
	tags, ok := f.identities[name]
	if !ok {
		return nil, &types.NotFoundException{Message: aws.String("no identity")}
	}
	status, verified := types.DkimStatusSuccess, true
	switch {
	case f.Failed[name]:
		status, verified = types.DkimStatusFailed, false
	case f.Unverified[name]:
		status, verified = types.DkimStatusPending, false
	}
	return &sesv2.GetEmailIdentityOutput{
		Tags:                     tags,
		VerifiedForSendingStatus: verified,
		DkimAttributes:           &types.DkimAttributes{Tokens: f.Tokens(name, f.generation(name)), Status: status},
	}, nil
}

// DeleteEmailIdentity implements SESAPI.
func (f *FakeSES) DeleteEmailIdentity(_ context.Context, in *sesv2.DeleteEmailIdentityInput, _ ...func(*sesv2.Options)) (*sesv2.DeleteEmailIdentityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	name := aws.ToString(in.EmailIdentity)
	if _, ok := f.identities[name]; !ok {
		return nil, &types.NotFoundException{Message: aws.String("no identity")}
	}
	delete(f.identities, name)
	f.Deleted = append(f.Deleted, name)
	return &sesv2.DeleteEmailIdentityOutput{}, nil
}

// Has reports whether the fake holds an identity for name.
func (f *FakeSES) Has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.identities[name]
	return ok
}

// LocalDNS answers every lookup as if the person had added the records
// mailgoose asked for, so domains verify on their first check in local runs,
// where nobody can change DNS. It never has a DMARC record.
type LocalDNS struct{ Region string }

func (d LocalDNS) notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// LookupCNAME implements Resolver.
func (d LocalDNS) LookupCNAME(_ context.Context, host string) (string, error) {
	if tok, _, ok := strings.Cut(host, "._domainkey."); ok {
		return tok + ".dkim.amazonses.com.", nil
	}
	return "", d.notFound(host)
}

// LookupMX implements Resolver.
func (d LocalDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if strings.HasPrefix(name, MailFromPrefix+".") {
		return []*net.MX{{Host: (&Service{Region: d.Region}).feedbackHost() + ".", Pref: 10}}, nil
	}
	return nil, d.notFound(name)
}

// LookupTXT implements Resolver.
func (d LocalDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if strings.HasPrefix(name, MailFromPrefix+".") {
		return []string{spfValue}, nil
	}
	return nil, d.notFound(name)
}
