package mail

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Fake records messages instead of sending them. Tests read Sent; local
// development points Dir at a folder and opens the .eml files it writes in a
// mail client or browser. It never touches the network.
type Fake struct {
	// Dir, when set, gets one .eml file per message.
	Dir string
	// Err, when set, is returned by every Send, to test failure paths.
	Err error

	mu   sync.Mutex
	sent []Sent
}

// Sent is a message the Fake recorded, with the ID it returned.
type Sent struct {
	ID      string
	Message Message
	At      time.Time
}

// Send implements Sender.
func (f *Fake) Send(_ context.Context, m Message) (string, error) {
	if f.Err != nil {
		return "", f.Err
	}
	if err := m.Validate(); err != nil {
		return "", &rejected{err: fmt.Errorf("fake: invalid message: %w", err)}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	id := fmt.Sprintf("fake-%d-%06d", now.Unix(), len(f.sent)+1)
	if f.Dir != "" {
		if err := os.MkdirAll(f.Dir, 0o750); err != nil {
			return "", fmt.Errorf("fake: %w", err)
		}
		name := filepath.Join(f.Dir, now.Format("20060102T150405.000000000Z")+"-"+id+".eml")
		if err := os.WriteFile(name, EML(m, id, now), 0o600); err != nil {
			return "", fmt.Errorf("fake: %w", err)
		}
	}
	f.sent = append(f.sent, Sent{ID: id, Message: m, At: now})
	return id, nil
}

// Sent returns a copy of every message recorded so far, oldest first.
func (f *Fake) Sent() []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Sent(nil), f.sent...)
}

// Last returns the most recent message, and false if there is none.
func (f *Fake) Last() (Sent, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return Sent{}, false
	}
	return f.sent[len(f.sent)-1], true
}

// Reset forgets every recorded message.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = nil
}

// EML renders m as an RFC 5322 message: multipart/alternative when it has
// both parts. It's what the Fake writes to disk; SES builds its own MIME.
func EML(m Message, id string, date time.Time) []byte {
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", m.From.String())
	h("To", m.To.String())
	for _, r := range m.ReplyTo {
		h("Reply-To", r.String())
	}
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", date.Format(time.RFC1123Z))
	h("Message-ID", "<"+id+"@fake.mailgoose.local>")
	h("MIME-Version", "1.0")
	for _, x := range m.Headers {
		h(x.Name, x.Value)
	}
	h("X-Mailgoose-Kind", string(m.Kind))

	part := func(w *multipart.Writer, ctype, body string) {
		pw, _ := w.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {ctype + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		qp := quotedprintable.NewWriter(pw)
		_, _ = qp.Write([]byte(body))
		_ = qp.Close()
	}

	switch {
	case m.HTML != "" && m.Text != "":
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		part(w, "text/plain", m.Text)
		part(w, "text/html", m.HTML)
		_ = w.Close()
		h("Content-Type", "multipart/alternative; boundary="+w.Boundary())
		b.WriteString("\r\n")
		b.Write(body.Bytes())
	default:
		ctype, text := "text/plain", m.Text
		if m.HTML != "" {
			ctype, text = "text/html", m.HTML
		}
		h("Content-Type", ctype+"; charset=utf-8")
		h("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		qp := quotedprintable.NewWriter(&b)
		_, _ = qp.Write([]byte(text))
		_ = qp.Close()
	}
	return b.Bytes()
}
