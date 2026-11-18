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
	"createCliToken": {
		{name: "right code", server: authServer, before: signedUp, req: cliTokenRequest(""), status: http.StatusCreated},
		{name: "malformed", server: authServer, req: jsonRequest(http.MethodPost, "/v1/cli-tokens", `[]`), status: http.StatusBadRequest},
		{name: "wrong code", server: authServer, before: signedUp, req: cliTokenRequest("wrong"), status: http.StatusUnprocessableEntity},
		{name: "rate limited", server: rateLimitedAuthServer, before: send(cliTokenRequest("wrong")),
			req: cliTokenRequest("wrong"), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: cliTokenRequest("wrong"), status: http.StatusServiceUnavailable},
	},
	"listApiKeys": {
		{name: "full key", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/api-keys?limit=10")), status: http.StatusOK},
		{name: "bad limit", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/api-keys?limit=0")), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/api-keys"), status: http.StatusUnauthorized},
		{name: "unknown key", server: authServer, req: bearer(request(http.MethodGet, "/v1/api-keys")), status: http.StatusUnauthorized},
		{name: "draft key", server: draftKey(authServer), req: bearer(request(http.MethodGet, "/v1/api-keys")), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/api-keys"))),
			req: bearer(request(http.MethodGet, "/v1/api-keys")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/api-keys")), status: http.StatusServiceUnavailable},
	},
	"createApiKey": {
		{name: "full key", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody)), status: http.StatusCreated},
		{name: "malformed", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/api-keys", `{"name":`)), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody), status: http.StatusUnauthorized},
		{name: "draft key", server: draftKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody)), status: http.StatusForbidden},
		{name: "bad scope", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/api-keys", `{"name":"x","scope":"admin"}`)), status: http.StatusUnprocessableEntity},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody))),
			req: bearer(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody)), status: http.StatusServiceUnavailable},
	},
	"revokeApiKey": {
		{name: "own key", server: fullKey(authServer), req: bearer(ownKeyPath), status: http.StatusNoContent},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/api-keys/nope")), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodDelete, "/v1/api-keys/"+noSuchID), status: http.StatusUnauthorized},
		{name: "draft key", server: draftKey(authServer), req: bearer(ownKeyPath), status: http.StatusForbidden},
		{name: "no such key", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/api-keys/"+noSuchID)), status: http.StatusNotFound},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodDelete, "/v1/api-keys/"+noSuchID))),
			req: bearer(request(http.MethodDelete, "/v1/api-keys/"+noSuchID)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodDelete, "/v1/api-keys/"+noSuchID)), status: http.StatusServiceUnavailable},
	},
	"getAccount": {
		{name: "draft key", server: draftKey(authServer), req: bearer(request(http.MethodGet, "/v1/account")), status: http.StatusOK},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/account"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/account"))),
			req: bearer(request(http.MethodGet, "/v1/account")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/account")), status: http.StatusServiceUnavailable},
	},
	"updateAccount": {
		{name: "full update", server: draftKey(authServer), req: bearer(jsonRequest(http.MethodPatch, "/v1/account", accountUpdate)), status: http.StatusOK},
		{name: "clear a field", server: draftKey(authServer), req: bearer(jsonRequest(http.MethodPatch, "/v1/account", `{"postal_address":null}`)), status: http.StatusOK},
		{name: "empty", server: draftKey(authServer), req: bearer(jsonRequest(http.MethodPatch, "/v1/account", `{}`)), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: jsonRequest(http.MethodPatch, "/v1/account", accountUpdate), status: http.StatusUnauthorized},
		{name: "bad time zone", server: draftKey(authServer), req: bearer(jsonRequest(http.MethodPatch, "/v1/account", `{"time_zone":"Mars/Olympus"}`)), status: http.StatusUnprocessableEntity},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(jsonRequest(http.MethodPatch, "/v1/account", accountUpdate))),
			req: bearer(jsonRequest(http.MethodPatch, "/v1/account", accountUpdate)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(jsonRequest(http.MethodPatch, "/v1/account", accountUpdate)), status: http.StatusServiceUnavailable},
	},
	"listTags": {
		{name: "listed", server: draftKey(authServer), before: addedTag, req: bearer(request(http.MethodGet, "/v1/tags?name=VIP&limit=5")), status: http.StatusOK},
		{name: "bad cursor", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/tags?cursor="+EncodeCursor("x"))), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/tags"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/tags"))), req: bearer(request(http.MethodGet, "/v1/tags")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/tags")), status: http.StatusServiceUnavailable},
	},
	"createTag": {
		{name: "created", server: draftKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"vip"}`)), status: http.StatusCreated},
		{name: "malformed", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":1}`)), status: http.StatusBadRequest},
		{name: "exists", server: fullKey(authServer), before: addedTag, req: bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"VIP"}`)), status: http.StatusConflict},
		{name: "bad name", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"a,b"}`)), status: http.StatusUnprocessableEntity},
		{name: "no credentials", server: authServer, req: jsonRequest(http.MethodPost, "/v1/tags", `{"name":"vip"}`), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"vip"}`))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/tags"))), req: bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"vip"}`)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"vip"}`)), status: http.StatusServiceUnavailable},
	},
	"renameTag": {
		{name: "renamed", server: draftKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodPatch, "", `{"name":"gold"}`)), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPatch, "/v1/tags/vip", `{"name":"gold"}`)), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(tagPath(http.MethodPatch, "", `{"name":"gold"}`)), status: http.StatusNotFound},
		{name: "name taken", server: fullKey(authServer), before: then(addedTag, send(bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"gold"}`)))),
			req: bearer(tagPath(http.MethodPatch, "", `{"name":"Gold"}`)), status: http.StatusConflict},
		{name: "bad name", server: fullKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodPatch, "", `{"name":" "}`)), status: http.StatusUnprocessableEntity},
		{name: "no credentials", server: authServer, req: tagPath(http.MethodPatch, "", `{"name":"gold"}`), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(tagPath(http.MethodPatch, "", `{"name":"gold"}`))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/tags"))), req: bearer(tagPath(http.MethodPatch, "", `{"name":"gold"}`)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(tagPath(http.MethodPatch, "", `{"name":"gold"}`)), status: http.StatusServiceUnavailable},
	},
	"deleteTag": {
		{name: "deleted", server: draftKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodDelete, "", "")), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/tags/vip")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(tagPath(http.MethodDelete, "", "")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: tagPath(http.MethodDelete, "", ""), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(tagPath(http.MethodDelete, "", ""))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/tags"))), req: bearer(tagPath(http.MethodDelete, "", "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(tagPath(http.MethodDelete, "", "")), status: http.StatusServiceUnavailable},
	},
	"tagContacts": {
		{name: "by filter", server: draftKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodPost, "/contacts", `{"filter":{"status":"subscribed"}}`)), status: http.StatusOK},
		{name: "malformed", server: fullKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodPost, "/contacts", `{"contact_ids":"x"}`)), status: http.StatusBadRequest},
		{name: "unknown tag", server: fullKey(authServer), req: bearer(tagPath(http.MethodPost, "/contacts", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`)), status: http.StatusNotFound},
		{name: "no selection", server: fullKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodPost, "/contacts", `{}`)), status: http.StatusUnprocessableEntity},
		{name: "no credentials", server: authServer, req: tagPath(http.MethodPost, "/contacts", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(tagPath(http.MethodPost, "/contacts", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/tags"))), req: bearer(tagPath(http.MethodPost, "/contacts", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(tagPath(http.MethodPost, "/contacts", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`)), status: http.StatusServiceUnavailable},
	},
	"untagContacts": {
		{name: "by filter", server: draftKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodPost, "/contacts/remove", `{"filter":{"status":"subscribed"}}`)), status: http.StatusOK},
		{name: "malformed", server: fullKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodPost, "/contacts/remove", `{"contact_ids":"x"}`)), status: http.StatusBadRequest},
		{name: "unknown tag", server: fullKey(authServer), req: bearer(tagPath(http.MethodPost, "/contacts/remove", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`)), status: http.StatusNotFound},
		{name: "no selection", server: fullKey(authServer), before: addedTag, req: bearer(tagPath(http.MethodPost, "/contacts/remove", `{}`)), status: http.StatusUnprocessableEntity},
		{name: "no credentials", server: authServer, req: tagPath(http.MethodPost, "/contacts/remove", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(tagPath(http.MethodPost, "/contacts/remove", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/tags"))), req: bearer(tagPath(http.MethodPost, "/contacts/remove", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(tagPath(http.MethodPost, "/contacts/remove", `{"contact_ids":["00000000-0000-4000-8000-000000000000"]}`)), status: http.StatusServiceUnavailable},
	},
	"listContacts": {
		{name: "filtered", server: draftKey(authServer), before: addedContact,
			req: bearer(request(http.MethodGet, "/v1/contacts?email=pat&name=x&status=subscribed&limit=5")), status: http.StatusOK},
		{name: "bad cursor", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/contacts?cursor="+EncodeCursor("x"))), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/contacts"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/contacts"))), req: bearer(request(http.MethodGet, "/v1/contacts")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/contacts")), status: http.StatusServiceUnavailable},
	},
	"exportContacts": {
		{name: "exported", server: draftKey(authServer), before: addedContact,
			req: bearer(request(http.MethodGet, "/v1/contacts/export?status=subscribed")), status: http.StatusOK},
		{name: "bad tag", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/contacts/export?tag_id=vip")), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/contacts/export"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/contacts"))), req: bearer(request(http.MethodGet, "/v1/contacts/export")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/contacts/export")), status: http.StatusServiceUnavailable},
	},
	"getContactData": {
		{name: "found", server: fullKey(authServer), before: addedContact, req: bearer(contactPathSuffix(http.MethodGet, "/data", "")), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/contacts/pat@example.com/data")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(contactPathSuffix(http.MethodGet, "/data", "")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: contactPathSuffix(http.MethodGet, "/data", ""), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/contacts"))), req: bearer(contactPathSuffix(http.MethodGet, "/data", "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(contactPathSuffix(http.MethodGet, "/data", "")), status: http.StatusServiceUnavailable},
	},
	"createContact": {
		{name: "created", server: draftKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com","first_name":"Pat"}`)), status: http.StatusCreated},
		{name: "malformed", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":1}`)), status: http.StatusBadRequest},
		{name: "already added", server: fullKey(authServer), before: addedContact,
			req: bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"PAT@example.com"}`)), status: http.StatusConflict},
		{name: "invalid email", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat"}`)), status: http.StatusUnprocessableEntity},
		{name: "over the free limit", server: atContactLimit(fullKey(authServer)), req: bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"over@example.com"}`)), status: http.StatusPaymentRequired},
		{name: "no credentials", server: authServer, req: jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com"}`), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn,
			req: fromAnotherSite(withSession(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com"}`))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/contacts"))), req: bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com"}`)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com"}`)), status: http.StatusServiceUnavailable},
	},
	"getContact": {
		{name: "found", server: fullKey(authServer), before: addedContact, req: bearer(contactPath(http.MethodGet, "")), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/contacts/pat@example.com")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(contactPath(http.MethodGet, "")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: contactPath(http.MethodGet, ""), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/contacts"))), req: bearer(contactPath(http.MethodGet, "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(contactPath(http.MethodGet, "")), status: http.StatusServiceUnavailable},
	},
	"updateContact": {
		{name: "changed", server: draftKey(authServer), before: addedContact, req: bearer(contactPath(http.MethodPatch, `{"last_name":"Lee"}`)), status: http.StatusOK},
		{name: "empty", server: fullKey(authServer), before: addedContact, req: bearer(contactPath(http.MethodPatch, `{}`)), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(contactPath(http.MethodPatch, `{"last_name":"Lee"}`)), status: http.StatusNotFound},
		{name: "address taken", server: fullKey(authServer), before: then(addedContact, send(bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"sam@example.com"}`)))),
			req: bearer(contactPath(http.MethodPatch, `{"email":"sam@example.com"}`)), status: http.StatusConflict},
		{name: "bounced status", server: fullKey(authServer), before: addedContact, req: bearer(contactPath(http.MethodPatch, `{"status":"bounced"}`)), status: http.StatusUnprocessableEntity},
		{name: "no credentials", server: authServer, req: contactPath(http.MethodPatch, `{"last_name":"Lee"}`), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(contactPath(http.MethodPatch, `{"last_name":"Lee"}`))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/contacts"))), req: bearer(contactPath(http.MethodPatch, `{"last_name":"Lee"}`)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(contactPath(http.MethodPatch, `{"last_name":"Lee"}`)), status: http.StatusServiceUnavailable},
	},
	"deleteContact": {
		{name: "deleted", server: draftKey(authServer), before: addedContact, req: bearer(contactPath(http.MethodDelete, "")), status: http.StatusNoContent},
		{name: "erased", server: fullKey(authServer), before: addedContact, req: bearer(contactPathSuffix(http.MethodDelete, "?erase=true", "")), status: http.StatusNoContent},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/contacts/x")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(contactPath(http.MethodDelete, "")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: contactPath(http.MethodDelete, ""), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(contactPath(http.MethodDelete, ""))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/contacts"))), req: bearer(contactPath(http.MethodDelete, "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(contactPath(http.MethodDelete, "")), status: http.StatusServiceUnavailable},
	},
	"createImport": {
		{name: "queued", server: draftKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/imports", importBody)), status: http.StatusAccepted},
		{name: "malformed", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/imports", `{"csv":1}`)), status: http.StatusBadRequest},
		{name: "no email column", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/imports", `{"csv":"name\nPat\n"}`)), status: http.StatusUnprocessableEntity},
		{name: "too large", server: fullKey(authServer), req: bearer(tooLargeImport), status: http.StatusRequestEntityTooLarge},
		{name: "no credentials", server: authServer, req: jsonRequest(http.MethodPost, "/v1/imports", importBody), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(jsonRequest(http.MethodPost, "/v1/imports", importBody))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(jsonRequest(http.MethodPost, "/v1/imports", importBody)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(jsonRequest(http.MethodPost, "/v1/imports", importBody)), status: http.StatusServiceUnavailable},
	},
	"listImports": {
		{name: "listed", server: draftKey(authServer), before: addedImport, req: bearer(request(http.MethodGet, "/v1/imports?limit=5")), status: http.StatusOK},
		{name: "bad cursor", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/imports?cursor="+EncodeCursor("x"))), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/imports"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(request(http.MethodGet, "/v1/imports")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/imports")), status: http.StatusServiceUnavailable},
	},
	"getImport": {
		{name: "found", server: draftKey(authServer), before: addedImport, req: bearer(importPath("")), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/imports/x")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(importPath("")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: importPath(""), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(importPath("")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(importPath("")), status: http.StatusServiceUnavailable},
	},
	"listImportSkippedRows": {
		{name: "listed", server: draftKey(authServer), before: addedImport, req: bearer(importPath("/skipped-rows?limit=5")), status: http.StatusOK},
		{name: "bad cursor", server: fullKey(authServer), before: addedImport, req: bearer(importPath("/skipped-rows?cursor=" + EncodeCursor("x"))), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(importPath("/skipped-rows")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: importPath("/skipped-rows"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(importPath("/skipped-rows")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(importPath("/skipped-rows")), status: http.StatusServiceUnavailable},
	},
	"listDomains": {
		{name: "draft key", server: draftKey(authServer), before: addedDomain, req: bearer(request(http.MethodGet, "/v1/domains")), status: http.StatusOK},
		{name: "by name", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/domains?name=example.com&limit=5")), status: http.StatusOK},
		{name: "bad cursor", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/domains?cursor="+EncodeCursor("x"))), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/domains"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/domains"))),
			req: bearer(request(http.MethodGet, "/v1/domains")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/domains")), status: http.StatusServiceUnavailable},
	},
	"createDomain": {
		{name: "created", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"example.com"}`)), status: http.StatusCreated},
		{name: "malformed", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/domains", `{"name":1}`)), status: http.StatusBadRequest},
		{name: "invalid name", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"com"}`)), status: http.StatusUnprocessableEntity},
		{name: "already added", server: fullKey(authServer), before: addedDomain,
			req: bearer(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"example.com"}`)), status: http.StatusConflict},
		{name: "no credentials", server: authServer, req: jsonRequest(http.MethodPost, "/v1/domains", `{"name":"example.com"}`), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn,
			req: fromAnotherSite(withSession(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"example.com"}`))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/domains"))),
			req: bearer(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"example.com"}`)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"example.com"}`)), status: http.StatusServiceUnavailable},
	},
	"getDomain": {
		{name: "found", server: fullKey(authServer), before: addedDomain, req: bearer(domainPath(http.MethodGet, "")), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/domains/example.com")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(domainPath(http.MethodGet, "")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: domainPath(http.MethodGet, ""), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/domains"))),
			req: bearer(domainPath(http.MethodGet, "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(domainPath(http.MethodGet, "")), status: http.StatusServiceUnavailable},
	},
	"deleteDomain": {
		{name: "deleted", server: fullKey(authServer), before: addedDomain, req: bearer(domainPath(http.MethodDelete, "")), status: http.StatusNoContent},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/domains/x")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(domainPath(http.MethodDelete, "")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: domainPath(http.MethodDelete, ""), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(domainPath(http.MethodDelete, ""))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/domains"))),
			req: bearer(domainPath(http.MethodDelete, "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(domainPath(http.MethodDelete, "")), status: http.StatusServiceUnavailable},
	},
	"verifyDomain": {
		{name: "checked", server: draftKey(authServer), before: addedDomain, req: bearer(domainPath(http.MethodPost, "/verify")), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodPost, "/v1/domains/x/verify")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(domainPath(http.MethodPost, "/verify")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: domainPath(http.MethodPost, "/verify"), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(domainPath(http.MethodPost, "/verify"))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/domains"))),
			req: bearer(domainPath(http.MethodPost, "/verify")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(domainPath(http.MethodPost, "/verify")), status: http.StatusServiceUnavailable},
	},
	"getCredits": {
		{name: "draft key", server: draftKey(authServer), req: bearer(request(http.MethodGet, "/v1/credits")), status: http.StatusOK},
		{name: "web session", server: authServer, before: signedIn, req: withSession(request(http.MethodGet, "/v1/credits")), status: http.StatusOK},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/credits"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/credits"))),
			req: bearer(request(http.MethodGet, "/v1/credits")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/credits")), status: http.StatusServiceUnavailable},
	},
	"listCreditTransactions": {
		{name: "web session", server: authServer, before: signedIn, req: withSession(request(http.MethodGet, "/v1/credits/transactions?limit=10")), status: http.StatusOK},
		{name: "bad cursor", server: draftKey(authServer), req: bearer(request(http.MethodGet, "/v1/credits/transactions?cursor="+EncodeCursor("x"))), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/credits/transactions"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/credits/transactions"))),
			req: bearer(request(http.MethodGet, "/v1/credits/transactions")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/credits/transactions")), status: http.StatusServiceUnavailable},
	},
	"createDeviceLogin": {
		{name: "no body", server: authServer, req: request(http.MethodPost, "/v1/device-logins"), status: http.StatusCreated},
		{name: "client name", server: authServer, req: jsonRequest(http.MethodPost, "/v1/device-logins", `{"client_name":"pat-laptop"}`), status: http.StatusCreated},
		{name: "malformed", server: authServer, req: jsonRequest(http.MethodPost, "/v1/device-logins", `{"client_name":1}`), status: http.StatusBadRequest},
		{name: "rate limited", server: rateLimitedAuthServer, before: send(request(http.MethodPost, "/v1/device-logins")),
			req: request(http.MethodPost, "/v1/device-logins"), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: request(http.MethodPost, "/v1/device-logins"), status: http.StatusServiceUnavailable},
	},
	"pollDeviceLogin": {
		{name: "approved", server: authServer, before: then(startedForSignedIn, send(withSession(userCodePath(http.MethodPost, "/approve")))), req: pollRequest, status: http.StatusOK},
		{name: "pending", server: authServer, before: startedDeviceLogin, req: pollRequest, status: http.StatusAccepted},
		{name: "denied", server: authServer, before: then(startedForSignedIn, send(withSession(userCodePath(http.MethodPost, "/deny")))), req: pollRequest, status: http.StatusForbidden},
		{name: "unknown", server: authServer, req: pollRequest, status: http.StatusGone},
		{name: "malformed", server: authServer, req: jsonRequest(http.MethodPost, "/v1/device-logins/token", `{}{}`), status: http.StatusBadRequest},
		{name: "too fast", server: authServer, before: then(startedDeviceLogin, send(pollRequest)), req: pollRequest, status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: pollRequest, status: http.StatusServiceUnavailable},
	},
	"getDeviceLogin": {
		{name: "pending", server: authServer, before: startedForSignedIn, req: withSession(userCodePath(http.MethodGet, "")), status: http.StatusOK},
		{name: "no session", server: authServer, before: startedDeviceLogin, req: userCodePath(http.MethodGet, ""), status: http.StatusUnauthorized},
		{name: "unknown code", server: authServer, before: signedIn, req: withSession(userCodePath(http.MethodGet, "")), status: http.StatusNotFound},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/whoami"))),
			req: bearer(userCodePath(http.MethodGet, "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: withSession(userCodePath(http.MethodGet, "")), status: http.StatusServiceUnavailable},
	},
	"approveDeviceLogin": {
		{name: "pending", server: authServer, before: startedForSignedIn, req: withSession(userCodePath(http.MethodPost, "/approve")), status: http.StatusNoContent},
		{name: "no session", server: authServer, before: startedDeviceLogin, req: userCodePath(http.MethodPost, "/approve"), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: startedForSignedIn, req: fromAnotherSite(withSession(userCodePath(http.MethodPost, "/approve"))), status: http.StatusForbidden},
		{name: "unknown code", server: authServer, before: signedIn, req: withSession(userCodePath(http.MethodPost, "/approve")), status: http.StatusNotFound},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/whoami"))),
			req: bearer(userCodePath(http.MethodPost, "/approve")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: withSession(userCodePath(http.MethodPost, "/approve")), status: http.StatusServiceUnavailable},
	},
	"denyDeviceLogin": {
		{name: "pending", server: authServer, before: startedForSignedIn, req: withSession(userCodePath(http.MethodPost, "/deny")), status: http.StatusNoContent},
		{name: "no session", server: authServer, before: startedDeviceLogin, req: userCodePath(http.MethodPost, "/deny"), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: startedForSignedIn, req: fromAnotherSite(withSession(userCodePath(http.MethodPost, "/deny"))), status: http.StatusForbidden},
		{name: "unknown code", server: authServer, before: signedIn, req: withSession(userCodePath(http.MethodPost, "/deny")), status: http.StatusNotFound},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/whoami"))),
			req: bearer(userCodePath(http.MethodPost, "/deny")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: withSession(userCodePath(http.MethodPost, "/deny")), status: http.StatusServiceUnavailable},
	},
	"revokeCurrentCliToken": {
		{name: "CLI token", server: withCLIToken(authServer), req: bearer(request(http.MethodDelete, "/v1/cli-tokens/current")), status: http.StatusNoContent},
		{name: "API key", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/cli-tokens/current")), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodDelete, "/v1/cli-tokens/current"), status: http.StatusUnauthorized},
		{name: "rate limited", server: withCLIToken(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/whoami"))),
			req: bearer(request(http.MethodDelete, "/v1/cli-tokens/current")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodDelete, "/v1/cli-tokens/current")), status: http.StatusServiceUnavailable},
	},
	"getWhoami": {
		{name: "draft key", server: draftKey(authServer), req: bearer(request(http.MethodGet, "/v1/whoami")), status: http.StatusOK},
		{name: "CLI token", server: withCLIToken(authServer), req: bearer(request(http.MethodGet, "/v1/whoami")), status: http.StatusOK},
		{name: "web session", server: authServer, before: signedIn, req: withSession(request(http.MethodGet, "/v1/whoami")), status: http.StatusOK},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/whoami"), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/whoami"))),
			req: bearer(request(http.MethodGet, "/v1/whoami")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/whoami")), status: http.StatusServiceUnavailable},
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

// accountUpdate sets every account setting.
const accountUpdate = `{"name":"Pat's Bakery","from_name":"Pat","from_email":"news@patsbakery.example",` +
	`"postal_address":"Pat's Bakery\n12 Main St\nSpringfield, IL 62701","time_zone":"America/Chicago"}`

const noSuchID = "00000000-0000-4000-8000-000000000000"

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
