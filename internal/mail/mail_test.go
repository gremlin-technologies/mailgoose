package mail

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/gremlin-technologies/mailgoose/internal/config"
)

func sample() Message {
	return Message{
		From:    Address{Name: "Zoë's Bakery", Email: "news@bakery.example"},
		To:      Address{Name: "Pat", Email: "pat@example.com"},
		ReplyTo: []Address{{Email: "hello@bakery.example"}},
		Subject: "Fresh bread — this week",
		HTML:    "<p>Hi Pat</p>",
		Text:    "Hi Pat",
		Headers: []Header{
			{"List-Unsubscribe", "<https://u.example/x>"},
			{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"},
		},
		Kind:       KindCampaign,
		AccountID:  "0b6c1d6e-1111-2222-3333-444455556666",
		CampaignID: "c_123",
	}
}

type fakeSES struct {
	in  *sesv2.SendEmailInput
	err error
}

func (f *fakeSES) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	return &sesv2.SendEmailOutput{MessageId: aws.String("0100018c-abc")}, nil
}

func TestSESSend(t *testing.T) {
	client := &fakeSES{}
	s := SES{Client: client, ConfigurationSet: "mailgoose-staging"}
	id, err := s.Send(context.Background(), sample())
	if err != nil {
		t.Fatal(err)
	}
	if id != "0100018c-abc" {
		t.Errorf("id = %q", id)
	}
	in := client.in
	if aws.ToString(in.ConfigurationSetName) != "mailgoose-staging" {
		t.Errorf("configuration set = %q", aws.ToString(in.ConfigurationSetName))
	}
	from, err := (&mime.WordDecoder{}).DecodeHeader(aws.ToString(in.FromEmailAddress))
	if err != nil || from != "Zoë's Bakery <news@bakery.example>" {
		t.Errorf("from = %q (%q), %v", aws.ToString(in.FromEmailAddress), from, err)
	}
	if got := in.Destination.ToAddresses; len(got) != 1 || got[0] != `"Pat" <pat@example.com>` {
		t.Errorf("to = %v", got)
	}
	if len(in.ReplyToAddresses) != 1 || in.ReplyToAddresses[0] != "<hello@bakery.example>" {
		t.Errorf("reply-to = %v", in.ReplyToAddresses)
	}

	tags := map[string]string{}
	for _, tg := range in.EmailTags {
		tags[aws.ToString(tg.Name)] = aws.ToString(tg.Value)
	}
	want := map[string]string{TagKind: "campaign", TagAccount: "0b6c1d6e-1111-2222-3333-444455556666", TagCampaign: "c_123"}
	for k, v := range want {
		if tags[k] != v {
			t.Errorf("tag %s = %q, want %q", k, tags[k], v)
		}
	}

	msg := in.Content.Simple
	subject := aws.ToString(msg.Subject.Data)
	for _, c := range subject {
		if c > 127 {
			t.Fatalf("subject not 7-bit: %q", subject)
		}
	}
	if dec, _ := (&mime.WordDecoder{}).DecodeHeader(subject); dec != "Fresh bread — this week" {
		t.Errorf("subject decodes to %q", dec)
	}
	if aws.ToString(msg.Body.Html.Data) != "<p>Hi Pat</p>" || aws.ToString(msg.Body.Text.Data) != "Hi Pat" {
		t.Error("body parts not passed through")
	}
	if len(msg.Headers) != 2 || aws.ToString(msg.Headers[0].Name) != "List-Unsubscribe" {
		t.Errorf("headers = %+v", msg.Headers)
	}
}

func TestSESTagValuesAreSanitized(t *testing.T) {
	client := &fakeSES{}
	m := sample()
	m.CampaignID = "camp/1 x"
	if _, err := (SES{Client: client, ConfigurationSet: "cs"}).Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	for _, tg := range client.in.EmailTags {
		if aws.ToString(tg.Name) == TagCampaign && aws.ToString(tg.Value) != "camp_1_x" {
			t.Errorf("campaign tag = %q", aws.ToString(tg.Value))
		}
	}
}

func TestSESErrors(t *testing.T) {
	if _, err := (SES{Client: &fakeSES{}}).Send(context.Background(), sample()); err == nil {
		t.Error("no configuration set should be an error")
	}
	client := &fakeSES{}
	bad := sample()
	bad.To.Email = "not an address"
	if _, err := (SES{Client: client, ConfigurationSet: "cs"}).Send(context.Background(), bad); err == nil || client.in != nil {
		t.Error("an invalid message must not reach SES")
	}
	boom := errors.New("throttled")
	_, err := (SES{Client: &fakeSES{err: boom}, ConfigurationSet: "cs"}).Send(context.Background(), sample())
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the SES error", err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Message){
		"bad from":         func(m *Message) { m.From.Email = "x" },
		"no to":            func(m *Message) { m.To.Email = "" },
		"bad reply-to":     func(m *Message) { m.ReplyTo = []Address{{Email: "@"}} },
		"no subject":       func(m *Message) { m.Subject = " " },
		"subject newline":  func(m *Message) { m.Subject = "a\r\nBcc: x@y.z" },
		"name newline":     func(m *Message) { m.From.Name = "a\nBcc: x" },
		"no body":          func(m *Message) { m.HTML, m.Text = "", "" },
		"reserved header":  func(m *Message) { m.Headers = []Header{{"Bcc", "x@y.z"}} },
		"header injection": func(m *Message) { m.Headers = []Header{{"X-A", "1\r\nBcc: x@y.z"}} },
		"bad header name":  func(m *Message) { m.Headers = []Header{{"X A", "1"}} },
		"unknown kind":     func(m *Message) { m.Kind = "promo" },
	}
	for name, mutate := range cases {
		m := sample()
		mutate(&m)
		if m.Validate() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := sample().Validate(); err != nil {
		t.Errorf("sample should be valid: %v", err)
	}
}

func TestFakeRecords(t *testing.T) {
	f := &Fake{}
	if _, ok := f.Last(); ok {
		t.Fatal("new fake has messages")
	}
	id1, err := f.Send(context.Background(), sample())
	if err != nil {
		t.Fatal(err)
	}
	m2 := sample()
	m2.To.Email = "sam@example.com"
	id2, _ := f.Send(context.Background(), m2)
	if id1 == id2 || id1 == "" {
		t.Errorf("ids %q %q should be distinct", id1, id2)
	}
	sent := f.Sent()
	if len(sent) != 2 || sent[0].ID != id1 || sent[1].Message.To.Email != "sam@example.com" {
		t.Errorf("sent = %+v", sent)
	}
	if last, _ := f.Last(); last.ID != id2 {
		t.Error("Last should be the second message")
	}
	f.Reset()
	if len(f.Sent()) != 0 {
		t.Error("Reset should clear")
	}

	bad := sample()
	bad.Subject = ""
	if _, err := f.Send(context.Background(), bad); err == nil {
		t.Error("fake should validate like SES")
	}
	f.Err = errors.New("down")
	if _, err := f.Send(context.Background(), sample()); err == nil {
		t.Error("Err should be returned")
	}
}

func TestFakeWritesEML(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	f := &Fake{Dir: dir}
	if _, err := f.Send(context.Background(), sample()); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.eml"))
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("not a valid message: %v", err)
	}
	if subj, _ := (&mime.WordDecoder{}).DecodeHeader(msg.Header.Get("Subject")); subj != "Fresh bread — this week" {
		t.Errorf("subject = %q", subj)
	}
	if msg.Header.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" {
		t.Error("custom header missing")
	}
	mt, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if mt != "multipart/alternative" {
		t.Fatalf("content type %q", mt)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, p.Header.Get("Content-Type"))
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Errorf("parts = %v", types)
	}
}

func TestEMLSinglePart(t *testing.T) {
	m := sample()
	m.Text = ""
	msg, err := mail.ReadMessage(strings.NewReader(string(EML(m, "x", time.Unix(0, 0)))))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(msg.Header.Get("Content-Type"), "text/html") {
		t.Errorf("content type %q", msg.Header.Get("Content-Type"))
	}
}

func TestNewPicksFakeWithoutAWS(t *testing.T) {
	s, err := New(context.Background(), config.Config{MailSender: "fake", MailCaptureDir: "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := s.(*Fake); !ok || f.Dir != "/tmp/x" {
		t.Errorf("got %T %+v", s, s)
	}
	if _, err := New(context.Background(), config.Config{MailSender: "smtp"}); err == nil {
		t.Error("unknown sender should be an error")
	}
}
