package mailchimp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAPI is a stand-in for Mailchimp's Marketing API, built from its public
// reference: lists, members, merge fields, interest categories and
// interests, segments and their members, campaigns and their content, and
// templates. Nothing in the tests reaches Mailchimp.
type fakeAPI struct {
	*httptest.Server
	mu        sync.Mutex
	lists     []*fakeList
	campaigns []fakeCampaign
	templates int

	delay       time.Duration
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	requests    atomic.Int32
	// tooMany and broken answer that many requests with 429 and 503 first.
	tooMany atomic.Int32
	broken  atomic.Int32
	// revokeAfter, when above 0, answers 401 once that many member pages
	// have been read.
	revokeAfter atomic.Int32
	memberPages atomic.Int32
}

type fakeList struct {
	id, name    string
	doubleOptin bool
	members     []fakeMember
	merge       [][2]string // tag, name
	categories  []fakeCategory
	segments    map[string]fakeSegment
}

type fakeMember struct {
	email, status string
	merge         map[string]any
	interests     map[string]bool
	tags          []string
	signup, opt   string
	ipSignup      string
	ipOpt         string
	permissions   []map[string]any
}

type fakeCategory struct {
	id, title string
	interests [][2]string // id, name
}

type fakeSegment struct {
	name    string
	members []string
}

type fakeCampaign struct {
	id, title, subject, sent, html, text string
}

var fakeRoutes = struct {
	list, members, merge, cats, interests, segment, segMembers, content *regexp.Regexp
}{
	list:       regexp.MustCompile(`^/3\.0/lists/([^/]+)$`),
	members:    regexp.MustCompile(`^/3\.0/lists/([^/]+)/members$`),
	merge:      regexp.MustCompile(`^/3\.0/lists/([^/]+)/merge-fields$`),
	cats:       regexp.MustCompile(`^/3\.0/lists/([^/]+)/interest-categories$`),
	interests:  regexp.MustCompile(`^/3\.0/lists/([^/]+)/interest-categories/([^/]+)/interests$`),
	segment:    regexp.MustCompile(`^/3\.0/lists/([^/]+)/segments/([^/]+)$`),
	segMembers: regexp.MustCompile(`^/3\.0/lists/([^/]+)/segments/([^/]+)/members$`),
	content:    regexp.MustCompile(`^/3\.0/campaigns/([^/]+)/content$`),
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve(t)))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(fakeToken, "us21", f.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Backoff = func(int) time.Duration { return time.Millisecond }
	return c
}

func (f *fakeAPI) list(id string) *fakeList {
	for _, l := range f.lists {
		if l.id == id {
			return l
		}
	}
	return nil
}

func page[T any](r *http.Request, all []T) ([]T, int) {
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if count <= 0 {
		count = 10
	}
	if count > 1000 {
		count = 1000
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset > len(all) {
		offset = len(all)
	}
	end := min(offset+count, len(all))
	return all[offset:end], len(all)
}

func (f *fakeAPI) serve(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		n := f.inFlight.Add(1)
		defer f.inFlight.Add(-1)
		for {
			m := f.maxInFlight.Load()
			if n <= m || f.maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		f.requests.Add(1)
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("the Marketing API got a %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"title":"API Key Invalid","status":401}`))
			return
		}
		if f.tooMany.Add(-1) >= 0 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"title":"Too Many Requests","status":429}`))
			return
		}
		if f.broken.Add(-1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		out, status := f.answer(r)
		w.WriteHeader(status)
		if out != nil {
			_ = json.NewEncoder(w).Encode(out)
		} else {
			_, _ = w.Write([]byte(`{"title":"Resource Not Found","status":404}`))
		}
	}
}

func (f *fakeAPI) answer(r *http.Request) (any, int) {
	p, q := r.URL.Path, r.URL.Query()
	type obj = map[string]any
	switch p {
	case "/3.0/lists":
		var ls []obj
		for _, l := range f.lists {
			counts := map[string]int{}
			for _, m := range l.members {
				counts[m.status]++
			}
			ls = append(ls, obj{"id": l.id, "name": l.name, "double_optin": l.doubleOptin, "stats": obj{
				"member_count": counts["subscribed"], "unsubscribe_count": counts["unsubscribed"],
				"cleaned_count": counts["cleaned"], "total_contacts": len(l.members)}})
		}
		got, total := page(r, ls)
		return obj{"lists": got, "total_items": total}, 200
	case "/3.0/campaigns":
		if q.Get("status") != "sent" || q.Get("sort_field") != "send_time" || q.Get("sort_dir") != "DESC" {
			return obj{"campaigns": []obj{}, "total_items": 0}, 200
		}
		cs := slices.Clone(f.campaigns)
		sort.Slice(cs, func(i, j int) bool { return cs[i].sent > cs[j].sent })
		var out []obj
		for _, c := range cs {
			out = append(out, obj{"id": c.id, "send_time": c.sent, "settings": obj{"subject_line": c.subject, "title": c.title}})
		}
		got, total := page(r, out)
		return obj{"campaigns": got, "total_items": total}, 200
	case "/3.0/templates":
		if q.Get("type") != "user" {
			return obj{"templates": []obj{}, "total_items": 0}, 200
		}
		return obj{"templates": []obj{}, "total_items": f.templates}, 200
	}
	if m := fakeRoutes.content.FindStringSubmatch(p); m != nil {
		for _, c := range f.campaigns {
			if c.id == m[1] {
				return obj{"html": c.html, "plain_text": c.text}, 200
			}
		}
		return nil, 404
	}
	if m := fakeRoutes.segMembers.FindStringSubmatch(p); m != nil {
		l := f.list(m[1])
		if l == nil {
			return nil, 404
		}
		s, ok := l.segments[m[2]]
		if !ok {
			return nil, 404
		}
		var ms []obj
		for _, e := range s.members {
			ms = append(ms, obj{"email_address": e})
		}
		got, total := page(r, ms)
		return obj{"members": got, "total_items": total}, 200
	}
	if m := fakeRoutes.segment.FindStringSubmatch(p); m != nil {
		l := f.list(m[1])
		if l == nil {
			return nil, 404
		}
		s, ok := l.segments[m[2]]
		if !ok {
			return nil, 404
		}
		id, _ := strconv.Atoi(m[2])
		return obj{"id": id, "name": s.name}, 200
	}
	if m := fakeRoutes.interests.FindStringSubmatch(p); m != nil {
		l := f.list(m[1])
		if l == nil {
			return nil, 404
		}
		for _, c := range l.categories {
			if c.id == m[2] {
				var is []obj
				for _, in := range c.interests {
					is = append(is, obj{"id": in[0], "name": in[1]})
				}
				return obj{"interests": is, "total_items": len(is)}, 200
			}
		}
		return nil, 404
	}
	if m := fakeRoutes.cats.FindStringSubmatch(p); m != nil {
		l := f.list(m[1])
		if l == nil {
			return nil, 404
		}
		var cs []obj
		for _, c := range l.categories {
			cs = append(cs, obj{"id": c.id, "title": c.title})
		}
		return obj{"categories": cs, "total_items": len(cs)}, 200
	}
	if m := fakeRoutes.merge.FindStringSubmatch(p); m != nil {
		l := f.list(m[1])
		if l == nil {
			return nil, 404
		}
		var ms []obj
		for _, mf := range l.merge {
			ms = append(ms, obj{"tag": mf[0], "name": mf[1]})
		}
		got, total := page(r, ms)
		return obj{"merge_fields": got, "total_items": total}, 200
	}
	if m := fakeRoutes.members.FindStringSubmatch(p); m != nil {
		l := f.list(m[1])
		if l == nil {
			return nil, 404
		}
		if q.Get("count") != "1" {
			if n := f.revokeAfter.Load(); n > 0 && f.memberPages.Load() >= n {
				return obj{"title": "API Key Invalid", "status": 401}, 401
			}
			f.memberPages.Add(1)
		}
		var ms []obj
		for _, mb := range l.members {
			if st := q.Get("status"); st != "" && mb.status != st {
				continue
			}
			tags := []obj{}
			for _, t := range mb.tags {
				tags = append(tags, obj{"id": 1, "name": t})
			}
			ms = append(ms, obj{"email_address": mb.email, "status": mb.status, "merge_fields": mb.merge, "interests": mb.interests,
				"tags": tags, "timestamp_signup": mb.signup, "ip_signup": mb.ipSignup, "timestamp_opt": mb.opt, "ip_opt": mb.ipOpt,
				"marketing_permissions": mb.permissions})
		}
		got, total := page(r, ms)
		return obj{"members": got, "total_items": total}, 200
	}
	if m := fakeRoutes.list.FindStringSubmatch(p); m != nil {
		l := f.list(m[1])
		if l == nil {
			return nil, 404
		}
		return obj{"id": l.id, "name": l.name, "double_optin": l.doubleOptin}, 200
	}
	return nil, 404
}

// bakery is an account with one audience of every kind of member, past the
// API's page size, and two campaigns.
func bakery(f *fakeAPI) {
	l := &fakeList{id: "a1b2c3", name: "Newsletter", doubleOptin: true,
		merge: [][2]string{{"FNAME", "First Name"}, {"LNAME", "Last Name"}, {"BIRTHDAY", "Birthday"}, {"MMERGE5", "Tags"},
			{"ADDRESS", "Address"}},
		categories: []fakeCategory{{id: "cat1", title: "Interests", interests: [][2]string{{"i1", "Bread"}, {"i2", "Cakes, tarts"}}}},
		segments:   map[string]fakeSegment{"40213": {name: "Regulars", members: []string{"ada@example.com", "BEA@example.com"}}},
	}
	l.members = append(l.members,
		fakeMember{email: "ada@example.com", status: "subscribed",
			merge:     map[string]any{"FNAME": "Ada", "LNAME": "Lovelace", "BIRTHDAY": "12/10", "MMERGE5": "x", "ADDRESS": map[string]any{"addr1": "1 Main St", "city": "Leeds"}},
			interests: map[string]bool{"i1": true, "i2": true}, tags: []string{"VIP", `Say "hi"`},
			signup: "2024-03-01T10:00:00+00:00", ipSignup: "203.0.113.5", opt: "2024-03-01T10:05:00+00:00", ipOpt: "203.0.113.6",
			permissions: []map[string]any{{"marketing_permission_id": "p1", "text": "Email", "enabled": true}}},
		fakeMember{email: "bea@example.com", status: "unsubscribed", merge: map[string]any{"FNAME": "Bea"},
			permissions: []map[string]any{{"marketing_permission_id": "p1", "text": "Email", "enabled": false}}},
		fakeMember{email: "cy@example.com", status: "cleaned"},
		fakeMember{email: "dee@example.com", status: "pending"},
		fakeMember{email: "eve@example.com", status: "transactional"},
		fakeMember{email: "flo@example.com", status: "archived"},
	)
	for i := range 2500 {
		l.members = append(l.members, fakeMember{email: fmt.Sprintf("bulk%04d@example.com", i), status: "subscribed"})
	}
	f.lists = append(f.lists, l, &fakeList{id: "z9y8x7", name: "Recipe Club"})
	f.campaigns = []fakeCampaign{
		{id: "c1", title: "Spring menu", subject: "Our spring menu", sent: "2026-04-01T09:00:00+00:00", html: "<p>Spring</p>", text: "Spring"},
		{id: "c2", title: "Summer menu", subject: "Our summer menu", sent: "2026-07-01T09:00:00+00:00", html: "<p>Summer</p>"},
	}
	f.templates = 3
}

func TestFetchReadsAnAccountIntoAnExport(t *testing.T) {
	f := newFakeAPI(t)
	bakery(f)
	var steps []string
	var last FetchProgress
	out, err := f.client(t).Fetch(context.Background(), FetchRequest{AudienceIDs: []string{"a1b2c3"}, SegmentIDs: []string{"40213"},
		Campaigns: 10}, func(p FetchProgress) error {
		if len(steps) == 0 || steps[len(steps)-1] != p.Step {
			steps = append(steps, p.Step)
		}
		last = p
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{StepAudiences, StepSegments, StepMembers, StepCampaigns}; !slices.Equal(steps, want) {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	if out.Members != 2506 || last.MembersTotal != 2506 {
		t.Errorf("members = %d of %d, want 2506", out.Members, last.MembersTotal)
	}
	if out.Templates != 3 || !slices.Equal(out.GroupColumns, []string{"Interests"}) {
		t.Errorf("templates %d, groups %v", out.Templates, out.GroupColumns)
	}
	if len(out.Segments) != 1 || out.Segments[0] != (SegmentTag{Audience: "Newsletter", Tag: "segment: Regulars", Members: 2}) {
		t.Errorf("segments = %+v", out.Segments)
	}
	if v, ok := out.DoubleOptin["Newsletter"]; !ok || !v {
		t.Errorf("double opt-in = %v", out.DoubleOptin)
	}

	b, err := Read(bytes.NewReader(out.Data), int64(len(out.Data)), FetchedFileName, Options{GroupColumns: out.GroupColumns})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Audiences) != 1 || b.Audiences[0].Name != "Newsletter" || len(b.Audiences[0].Members) != 2506 {
		t.Fatalf("audiences = %+v", b.Audiences)
	}
	a := b.Audiences[0]
	byEmail := map[string]*Member{}
	statuses := map[Status]int{}
	for _, m := range a.Members {
		byEmail[m.Email] = m
		statuses[m.Status]++
	}
	if statuses[Subscribed] != 2501 || statuses[Unsubscribed] != 1 || statuses[Cleaned] != 1 || statuses[Pending] != 1 ||
		statuses[Transactional] != 1 || statuses[Archived] != 1 {
		t.Errorf("statuses = %v", statuses)
	}
	ada := byEmail["ada@example.com"]
	if ada.FirstName != "Ada" || ada.LastName != "Lovelace" {
		t.Errorf("ada = %+v", ada)
	}
	if want := []string{"VIP", "Say 'hi'", "segment: Regulars"}; !slices.Equal(ada.Tags, want) {
		t.Errorf("ada's tags = %q, want %q", ada.Tags, want)
	}
	if want := []Group{{"Interests", "Bread"}, {"Interests", "Cakes, tarts"}}; !slices.Equal(ada.Groups, want) {
		t.Errorf("ada's groups = %v", ada.Groups)
	}
	if ada.OptinIP != "203.0.113.5" || ada.ConfirmIP != "203.0.113.6" || ada.OptinAt == nil || ada.ConfirmAt == nil ||
		ada.ConfirmAt.Format(time.RFC3339) != "2024-03-01T10:05:00Z" {
		t.Errorf("ada's opt-in = %+v", ada)
	}
	if ada.RefusesEmail() || !ada.PermissionsKnown {
		t.Errorf("ada granted email: %+v", ada.Permissions)
	}
	bea := byEmail["bea@example.com"]
	if !bea.RefusesEmail() || !slices.Equal(bea.Tags, []string{"segment: Regulars"}) {
		t.Errorf("bea = %+v", bea)
	}
	if byEmail["cy@example.com"].PermissionsKnown {
		t.Error("a member with no GDPR data shouldn't refuse email")
	}
	// The merge fields come in Mailchimp's order, and the one named Tags
	// is a merge field, not read as tags (MG-132).
	if want := []string{"Birthday", "Tags (MMERGE5)", "Address"}; !slices.Equal(a.MergeFields, want) || len(a.DroppedFields) != 0 {
		t.Errorf("merge fields = %v, want %v; dropped %v", a.MergeFields, want, a.DroppedFields)
	}
	// Each merge field's tag comes over too (MG-134).
	if want := []string{"BIRTHDAY", "MMERGE5", "ADDRESS"}; !slices.Equal(a.MergeTags, want) {
		t.Errorf("merge tags = %v, want %v", a.MergeTags, want)
	}
	// An address is one text value, its parts joined.
	if want := []string{"12/10", "x", "1 Main St, Leeds"}; !slices.Equal(ada.Merge, want) {
		t.Errorf("ada's merge values = %q, want %q", ada.Merge, want)
	}
	if bea.Merge != nil {
		t.Errorf("bea has no merge values: %q", bea.Merge)
	}
	if len(b.Campaigns) != 2 {
		t.Fatalf("campaigns = %+v", b.Campaigns)
	}
	for _, c := range b.Campaigns {
		if c.Subject == "" || c.SentAt == nil {
			t.Errorf("campaign %s has no subject or send time: %+v", c.Name, c)
		}
	}
	if b.Campaigns[0].Name != "c1" || b.Campaigns[0].Text != "Spring" || b.Campaigns[0].HTML != "<p>Spring</p>" {
		t.Errorf("campaign c1 = %+v", b.Campaigns[0])
	}
	if len(b.Ignored) != 0 {
		t.Errorf("ignored = %v", b.Ignored)
	}
}

func TestFetchNamesEachAudienceApart(t *testing.T) {
	f := newFakeAPI(t)
	f.lists = []*fakeList{
		{id: "l1", name: "News", members: []fakeMember{{email: "a@example.com", status: "subscribed"}}},
		{id: "l2", name: "News", members: []fakeMember{{email: "b@example.com", status: "subscribed"}}},
		{id: "l3", name: "reports/2024"},
		{id: "l4", name: "Reports"},
	}
	out, err := f.client(t).Fetch(context.Background(), FetchRequest{AudienceIDs: []string{"l1", "l2", "l3", "l4"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Read(bytes.NewReader(out.Data), int64(len(out.Data)), FetchedFileName, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range b.Audiences {
		names = append(names, a.Name)
	}
	if want := []string{"News", "News (2)", "Reports audience", "reports-2024"}; !slices.Equal(names, want) {
		t.Errorf("audiences = %q, want %q", names, want)
	}
}

func TestFetchRefusesWhatItCantRead(t *testing.T) {
	f := newFakeAPI(t)
	bakery(f)
	c := f.client(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		req  FetchRequest
		code string
	}{
		{"unknown audience", FetchRequest{AudienceIDs: []string{"nope"}}, ErrUnknownAudience},
		{"segment of another audience", FetchRequest{AudienceIDs: []string{"z9y8x7"}, SegmentIDs: []string{"40213"}}, ErrUnknownSegment},
		{"too many members", FetchRequest{AudienceIDs: []string{"a1b2c3"}, MaxMembers: 100}, ErrTooManyMembers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Fetch(ctx, tc.req, nil)
			var re *Error
			if !errors.As(err, &re) || re.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestTheClientRetriesBusyAnswers(t *testing.T) {
	f := newFakeAPI(t)
	bakery(f)
	c := f.client(t)
	ctx := context.Background()

	f.tooMany.Store(2)
	f.broken.Store(1)
	if _, err := c.Audiences(ctx); err != nil {
		t.Fatalf("after two 429s and a 503: %v", err)
	}
	if n := f.requests.Load(); n != 4 {
		t.Errorf("%d requests, want 4", n)
	}

	f.requests.Store(0)
	f.broken.Store(100)
	_, err := c.Audiences(ctx)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
	if n := f.requests.Load(); n != maxAttempts {
		t.Errorf("%d requests, want %d", n, maxAttempts)
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Error("the error holds the token")
	}
}

func TestTheClientStopsWhenAccessIsRevoked(t *testing.T) {
	f := newFakeAPI(t)
	bakery(f)
	f.revokeAfter.Store(1)
	_, err := f.client(t).Fetch(context.Background(), FetchRequest{AudienceIDs: []string{"a1b2c3"}}, nil)
	if !errors.Is(err, ErrAccessRevoked) {
		t.Fatalf("err = %v, want ErrAccessRevoked", err)
	}
	if n := f.memberPages.Load(); n != 1 {
		t.Errorf("read %d member pages after the 401, want it to stop", n-1)
	}
}

func TestTheClientKeepsToTenConnections(t *testing.T) {
	f := newFakeAPI(t)
	f.delay = 20 * time.Millisecond
	// Two clients for the same token share the limit.
	clients := []*Client{f.client(t), f.client(t)}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			if err := clients[i%2].Get(context.Background(), "/3.0/templates", nil, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if m := f.maxInFlight.Load(); m > MaxConnections || m < 2 {
		t.Errorf("at most %d requests in flight, want 2 to %d", m, MaxConnections)
	}
}
