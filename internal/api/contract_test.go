package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
)

// specPath is the API contract, relative to this package.
const specPath = "../../api/openapi.yaml"

// contractCase is one request against a test server, expected to get one of
// the responses the spec documents.
type contractCase struct {
	name   string
	server func(t *testing.T) *Server
	// before, if set, sends requests through the same handler first, for
	// cases such as rate limiting that need earlier requests.
	before func(t *testing.T, h http.Handler)
	req    func(t *testing.T) *http.Request
	status int
}

// contractCases lists, for every operationId in the spec, requests that
// exercise each response the operation documents. TestContract fails when an
// operation has no entry here, when an entry leaves a documented status code
// untested, when the request reaches no handler, and when a response doesn't
// match the spec. Add an entry with every new operation.
var contractCases = map[string][]contractCase{
	"getHealth": {
		{name: "up", server: serverWith(nil), req: request(http.MethodGet, "/healthz"), status: http.StatusOK},
	},
	"getReadiness": {
		{name: "database up", server: serverWith(pinger{}), req: request(http.MethodGet, "/readyz"), status: http.StatusOK},
		{name: "database down", server: serverWith(pinger{err: errors.New("down")}), req: request(http.MethodGet, "/readyz"), status: http.StatusServiceUnavailable},
		{name: "no database", server: serverWith(nil), req: request(http.MethodGet, "/readyz"), status: http.StatusServiceUnavailable},
	},
	"createSignup": {
		{name: "new address", server: authServer, req: jsonRequest(http.MethodPost, "/v1/signup", signupBody), status: http.StatusAccepted},
		{name: "malformed", server: authServer, req: jsonRequest(http.MethodPost, "/v1/signup", `{"email":`), status: http.StatusBadRequest},
		{name: "terms not accepted", server: authServer, req: jsonRequest(http.MethodPost, "/v1/signup", `{"email":"pat@example.com","account_name":"Pat","accepted_terms":false}`), status: http.StatusUnprocessableEntity},
		{name: "rate limited", server: rateLimitedAuthServer, before: send(jsonRequest(http.MethodPost, "/v1/signup", signupBody)),
			req: jsonRequest(http.MethodPost, "/v1/signup", signupBody), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: jsonRequest(http.MethodPost, "/v1/signup", signupBody), status: http.StatusServiceUnavailable},
	},
	"createSession": {
		{name: "right code", server: authServer, before: signedUp, req: sessionRequest(""), status: http.StatusCreated},
		{name: "malformed", server: authServer, req: jsonRequest(http.MethodPost, "/v1/sessions", `[]`), status: http.StatusBadRequest},
		{name: "wrong code", server: authServer, before: signedUp, req: sessionRequest("wrong"), status: http.StatusUnprocessableEntity},
		{name: "rate limited", server: rateLimitedAuthServer, before: send(sessionRequest("wrong")),
			req: sessionRequest("wrong"), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: sessionRequest("wrong"), status: http.StatusServiceUnavailable},
	},
	"createSignIn": {
		{name: "known address", server: authServer, before: signedIn, req: jsonRequest(http.MethodPost, "/v1/sign-in", signInBody), status: http.StatusAccepted},
		{name: "unknown address", server: authServer, req: jsonRequest(http.MethodPost, "/v1/sign-in", signInBody), status: http.StatusAccepted},
		{name: "malformed", server: authServer, req: jsonRequest(http.MethodPost, "/v1/sign-in", `{"email":1}`), status: http.StatusBadRequest},
		{name: "bad address", server: authServer, req: jsonRequest(http.MethodPost, "/v1/sign-in", `{"email":"not an address"}`), status: http.StatusUnprocessableEntity},
		{name: "rate limited", server: rateLimitedAuthServer, before: send(jsonRequest(http.MethodPost, "/v1/sign-in", signInBody)),
			req: jsonRequest(http.MethodPost, "/v1/sign-in", signInBody), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: jsonRequest(http.MethodPost, "/v1/sign-in", signInBody), status: http.StatusServiceUnavailable},
	},
	"getCurrentSession": {
		{name: "signed in", server: authServer, before: signedIn, req: withSession(request(http.MethodGet, "/v1/sessions/current")), status: http.StatusOK},
		{name: "no cookie", server: authServer, req: request(http.MethodGet, "/v1/sessions/current"), status: http.StatusUnauthorized},
		{name: "unknown session", server: authServer, req: withSession(request(http.MethodGet, "/v1/sessions/current")), status: http.StatusUnauthorized},
		{name: "no database", server: serverWith(nil), req: withSession(request(http.MethodGet, "/v1/sessions/current")), status: http.StatusServiceUnavailable},
	},
	"deleteCurrentSession": {
		{name: "signed in", server: authServer, before: signedIn, req: withSession(request(http.MethodDelete, "/v1/sessions/current")), status: http.StatusNoContent},
		{name: "no cookie", server: authServer, req: request(http.MethodDelete, "/v1/sessions/current"), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(request(http.MethodDelete, "/v1/sessions/current"))), status: http.StatusForbidden},
		{name: "no database", server: serverWith(nil), req: withSession(request(http.MethodDelete, "/v1/sessions/current")), status: http.StatusServiceUnavailable},
	},
	"deleteAllSessions": {
		{name: "signed in", server: authServer, before: signedIn, req: withSession(request(http.MethodDelete, "/v1/sessions")), status: http.StatusNoContent},
		{name: "no cookie", server: authServer, req: request(http.MethodDelete, "/v1/sessions"), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(request(http.MethodDelete, "/v1/sessions"))), status: http.StatusForbidden},
		{name: "no database", server: serverWith(nil), req: withSession(request(http.MethodDelete, "/v1/sessions")), status: http.StatusServiceUnavailable},
	},
}

func serverWith(db Pinger) func(*testing.T) *Server {
	return func(*testing.T) *Server { return newServer(db) }
}

func request(method, path string) func(*testing.T) *http.Request {
	return func(*testing.T) *http.Request { return httptest.NewRequest(method, path, nil) }
}

func jsonRequest(method, path, body string) func(*testing.T) *http.Request {
	return func(*testing.T) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
}

// send runs req through the handler and ignores the answer.
func send(req func(*testing.T) *http.Request) func(*testing.T, http.Handler) {
	return func(t *testing.T, h http.Handler) { h.ServeHTTP(httptest.NewRecorder(), req(t)) }
}

type specOperation struct {
	id, method, path string
	statuses         []int
}

func loadContract(t *testing.T) (validator.Validator, []specOperation) {
	t.Helper()
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("build spec model: %v", err)
	}
	var ops []specOperation
	for path, item := range model.Model.Paths.PathItems.FromOldest() {
		for method, op := range item.GetOperations().FromOldest() {
			o := specOperation{id: op.OperationId, method: strings.ToUpper(method), path: path}
			if op.OperationId == "" {
				t.Errorf("%s %s has no operationId", o.method, path)
			}
			for code := range op.Responses.Codes.KeysFromOldest() {
				n, err := strconv.Atoi(code)
				if err != nil {
					t.Errorf("%s: response %q: use explicit status codes", op.OperationId, code)
					continue
				}
				o.statuses = append(o.statuses, n)
			}
			ops = append(ops, o)
		}
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		t.Fatalf("build validator: %v", errs)
	}
	return v, ops
}

// TestContract runs every operation in api/openapi.yaml against a test server.
func TestContract(t *testing.T) {
	v, ops := loadContract(t)
	if len(ops) == 0 {
		t.Fatal("the spec has no operations")
	}
	known := map[string]bool{}
	for _, op := range ops {
		known[op.id] = true
		t.Run(op.id, func(t *testing.T) {
			cases, ok := contractCases[op.id]
			if !ok {
				t.Fatalf("%s %s has no entry in contractCases", op.method, op.path)
			}
			tested := map[int]bool{}
			skipped := false
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					defer func() {
						if t.Skipped() {
							skipped = true
						}
					}()
					s := c.server(t)
					h := s.Handler()
					if c.before != nil {
						c.before(t, h)
					}
					req := c.req(t)
					if req.Method != op.method {
						t.Fatalf("case sends %s, operation is %s", req.Method, op.method)
					}
					if _, pattern := s.routes().Handler(req); pattern == "/" || pattern == "" {
						t.Fatalf("%s %s reaches no handler", req.Method, req.URL.Path)
					}

					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					resp := rec.Result()
					defer func() { _ = resp.Body.Close() }()
					if resp.StatusCode != c.status {
						t.Fatalf("status = %d, want %d; body %s", resp.StatusCode, c.status, rec.Body.String())
					}
					if !slices.Contains(op.statuses, resp.StatusCode) {
						t.Fatalf("status %d isn't documented for %s", resp.StatusCode, op.id)
					}
					if ok, verrs := v.ValidateHttpResponse(req, resp); !ok {
						for _, e := range verrs {
							t.Errorf("response doesn't match the spec: %s (%s)", e.Message, e.Reason)
							for _, se := range e.SchemaValidationErrors {
								t.Errorf("  %s: %s", se.FieldPath, se.Reason)
							}
						}
					}
					tested[resp.StatusCode] = true
				})
			}
			if skipped {
				t.Log("some cases were skipped (no test database), so response coverage isn't checked")
				return
			}
			for _, code := range op.statuses {
				if !tested[code] {
					t.Errorf("documented response %d has no passing contract case", code)
				}
			}
		})
	}
	for id := range contractCases {
		if !known[id] {
			t.Errorf("contractCases has %q, which isn't in the spec", id)
		}
	}
}

// TestContractValidatorCatchesMismatch guards TestContract itself: a response
// that breaks the spec must fail validation.
func TestContractValidatorCatchesMismatch(t *testing.T) {
	v, _ := loadContract(t)
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"wrong enum":          {http.StatusOK, `{"status":"maybe","version":"v1"}`},
		"missing field":       {http.StatusOK, `{"status":"ok"}`},
		"undocumented status": {http.StatusTeapot, `{"status":"ok","version":"v1"}`},
		"error without hint":  {http.StatusServiceUnavailable, `{"error":{"code":"x","message":"y"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()
			rec.Header().Set("Content-Type", "application/json")
			rec.WriteHeader(tc.status)
			_, _ = rec.WriteString(tc.body)
			if ok, _ := v.ValidateHttpResponse(req, rec.Result()); ok {
				t.Error("validator accepted a response that breaks the spec")
			}
		})
	}
}
