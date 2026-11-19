// Package tracking counts opens and clicks (REP-3) with mailgoose's own
// pixel and redirects; SES's tracking is off, so a campaign with tracking
// off really has none.
//
// A tracked copy's links point at /t/c/{token}, and it carries a 1x1 image
// at /t/o/{token}. A token names the account, the campaign and the send
// record (campaign_recipients row), and for a click the link's position in
// the email and where it goes. It's signed with the account's link key
// (the one unsubscribe links use, under another label), so a link can't be
// changed to point somewhere else: a changed one gets an error page, never
// a redirect. Nothing is looked up to redirect, only the key.
package tracking

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/render"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

// Paths the pixel and redirects are served at, under the app's URL.
const (
	OpenPrefix  = "/t/o/"
	ClickPrefix = "/t/c/"
)

// Event types.
const (
	TypeOpen  = "open"
	TypeClick = "click"
)

// MaxURL is the longest link that's tracked; a longer one is left as it
// is, so the tracked address stays well under what mail apps cut off.
const MaxURL = 2000

// ErrBadToken means a token was cut short, changed, or isn't one mailgoose
// made.
var ErrBadToken = errors.New("that link isn't valid")

const (
	tokenPrefix = "t1"
	macLen      = 16
	// header: account, campaign, send record, link number.
	headerLen  = 16 + 16 + 8 + 2
	openLabel  = "mailgoose-open"
	clickLabel = "mailgoose-click"
)

// Links makes one campaign's tracked addresses.
type Links struct {
	signer   *unsubscribe.Signer
	base     string
	campaign uuid.UUID
}

// NewLinks makes the tracked addresses for a campaign, under baseURL (the
// app's public URL).
func NewLinks(signer *unsubscribe.Signer, baseURL, campaignID string) (*Links, error) {
	c, err := uuid.Parse(campaignID)
	if err != nil {
		return nil, err
	}
	return &Links{signer: signer, base: strings.TrimRight(baseURL, "/"), campaign: c}, nil
}

func (l *Links) header(recipientID int64, link int) []byte {
	b := make([]byte, 0, headerLen+MaxURL+macLen)
	account, _ := uuid.Parse(l.signer.AccountID())
	b = append(b, account[:]...)
	b = append(b, l.campaign[:]...)
	b = binary.BigEndian.AppendUint64(b, uint64(recipientID)) //nolint:gosec // send record ids are positive (bigserial)
	return binary.BigEndian.AppendUint16(b, uint16(link))     //nolint:gosec // Click and Open keep link in 0..65535
}

func encode(body []byte) string { return tokenPrefix + base64.RawURLEncoding.EncodeToString(body) }

// Open is the pixel's address for a send record.
func (l *Links) Open(recipientID int64) string {
	body := l.header(recipientID, 0)
	body = append(body, l.signer.Sum(openLabel, body)[:macLen]...)
	return l.base + OpenPrefix + encode(body)
}

// Click is the tracked address of the link-th link (from 1) going to
// target, or target itself when it isn't one to track.
func (l *Links) Click(recipientID int64, link int, target string) string {
	if !Trackable(target) || link < 1 || link > 0xffff {
		return target
	}
	body := l.header(recipientID, link)
	body = append(body, target...)
	body = append(body, l.signer.Sum(clickLabel, body)[:macLen]...)
	return l.base + ClickPrefix + encode(body)
}

// Trackable reports whether a link is one to track: an absolute http or
// https address, of a sane length.
func Trackable(target string) bool {
	if len(target) > MaxURL {
		return false
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// Hit is a checked open or click.
type Hit struct {
	AccountID   string
	CampaignID  string
	RecipientID int64
	// Link is the link's position in the email, from 1; 0 for an open.
	Link int
	// URL is where a click goes.
	URL string
}

// Service checks tokens and records opens and clicks.
type Service struct {
	DB          *pgxpool.Pool
	Unsubscribe *unsubscribe.Service
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	keys sync.Map // account id -> *unsubscribe.Signer; keys never change

	missMu sync.Mutex
	misses map[string]time.Time // account id -> until when it's known keyless
}

// Accounts with no key are remembered for missTTL, at most maxMisses of
// them, so forged tokens naming them don't each cost a database read.
const (
	missTTL   = time.Minute
	maxMisses = 10000
)

func (s *Service) knownKeyless(accountID string, now time.Time) bool {
	s.missMu.Lock()
	defer s.missMu.Unlock()
	until, ok := s.misses[accountID]
	if ok && now.After(until) {
		delete(s.misses, accountID)
		return false
	}
	return ok
}

func (s *Service) rememberKeyless(accountID string, now time.Time) {
	s.missMu.Lock()
	defer s.missMu.Unlock()
	if s.misses == nil || len(s.misses) >= maxMisses {
		s.misses = map[string]time.Time{}
	}
	s.misses[accountID] = now.Add(missTTL)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) signer(ctx context.Context, accountID string) (*unsubscribe.Signer, error) {
	if v, ok := s.keys.Load(accountID); ok {
		return v.(*unsubscribe.Signer), nil
	}
	now := s.now()
	if s.knownKeyless(accountID, now) {
		return nil, ErrBadToken
	}
	g, err := s.Unsubscribe.Existing(ctx, accountID)
	if errors.Is(err, unsubscribe.ErrNoKey) {
		s.rememberKeyless(accountID, now)
		return nil, ErrBadToken
	}
	if err != nil {
		return nil, err
	}
	s.keys.Store(accountID, g)
	return g, nil
}

// Check checks a token from the open (click false) or click address and
// returns what it's for, or ErrBadToken.
func (s *Service) Check(ctx context.Context, token string, click bool) (*Hit, error) {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok {
		return nil, ErrBadToken
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(rest)
	if err != nil || len(body) < headerLen+macLen {
		return nil, ErrBadToken
	}
	msg, sum := body[:len(body)-macLen], body[len(body)-macLen:]
	if !click && len(msg) != headerLen {
		return nil, ErrBadToken
	}
	rcpt := binary.BigEndian.Uint64(msg[32:40])
	if rcpt > math.MaxInt64 {
		return nil, ErrBadToken
	}
	account, _ := uuid.FromBytes(msg[:16])
	campaign, _ := uuid.FromBytes(msg[16:32])
	h := &Hit{AccountID: account.String(), CampaignID: campaign.String(),
		RecipientID: int64(rcpt), Link: int(binary.BigEndian.Uint16(msg[40:42])),
		URL: string(msg[headerLen:])}
	g, err := s.signer(ctx, h.AccountID)
	if err != nil {
		return nil, err
	}
	label := openLabel
	if click {
		label = clickLabel
	}
	if !hmac.Equal(sum, g.Sum(label, msg)[:macLen]) {
		return nil, ErrBadToken
	}
	if click && (h.Link < 1 || !Trackable(h.URL)) || !click && h.Link != 0 {
		return nil, ErrBadToken
	}
	return h, nil
}

// Record stores an open or click. A click counts as an open too when the
// copy has none yet (the pixel was blocked). A send record that's gone
// (the campaign or account deleted) records nothing.
func (s *Service) Record(ctx context.Context, h *Hit) error {
	typ := TypeOpen
	var link *int
	var target *string
	if h.Link > 0 {
		stored := StoredURL(h.URL)
		typ, link, target = TypeClick, &h.Link, &stored
	}
	_, err := s.DB.Exec(ctx, `
		WITH r AS (
			SELECT id, account_id, campaign_id FROM campaign_recipients
			WHERE id = $1 AND campaign_id = $2 AND account_id = $3
		), hit AS (
			INSERT INTO engagement_events (account_id, campaign_id, recipient_id, type, link, url, occurred_at)
			SELECT account_id, campaign_id, id, $4, $5, $6, $7 FROM r
		)
		INSERT INTO engagement_events (account_id, campaign_id, recipient_id, type, occurred_at)
		SELECT account_id, campaign_id, id, 'open', $7 FROM r
		WHERE $4 = 'click' AND NOT EXISTS (SELECT 1 FROM engagement_events e WHERE e.recipient_id = r.id AND e.type = 'open')`,
		h.RecipientID, h.CampaignID, h.AccountID, typ, link, target, s.now())
	return err
}

// StoredURL is the part of a clicked link kept with the click. Links are
// tracked after merge tags are filled in, so the query, fragment and any
// user info, where merge tags usually land, are dropped; a path holding
// an address keeps only the site. The click itself still goes to the whole
// link.
func StoredURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	site := u.Scheme + "://" + u.Host
	if strings.Contains(u.Path, "@") { // decoded, so %40 too
		return site
	}
	return site + u.EscapedPath()
}

// For is one copy's tracked addresses, for render.Track.
func (l *Links) For(recipientID int64) render.Tracker { return copyLinks{l, recipientID} }

type copyLinks struct {
	l  *Links
	id int64
}

func (c copyLinks) Click(n int, target string) string { return c.l.Click(c.id, n, target) }
func (c copyLinks) Pixel() string                     { return c.l.Open(c.id) }

// ClickTarget is where one of mailgoose's tracked links (/t/c/...) goes,
// read from the token without checking its signature, for looking the
// target up (MG-64). ok is false for any other link.
func ClickTarget(link string) (target string, ok bool) {
	u, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	token, found := strings.CutPrefix(u.Path, ClickPrefix)
	if !found {
		return "", false
	}
	rest, found := strings.CutPrefix(token, tokenPrefix)
	if !found {
		return "", false
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(rest)
	if err != nil || len(body) <= headerLen+macLen {
		return "", false
	}
	target = string(body[headerLen : len(body)-macLen])
	if !Trackable(target) {
		return "", false
	}
	return target, true
}
