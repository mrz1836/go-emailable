package emailable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Batch-creation bounds.
const (
	// maxBatchRequestBytes caps the encoded POST /v1/batch body, whatever
	// WithMaxBatchEmails allows.
	maxBatchRequestBytes = 64 << 20

	maxCallbackURLBytes = 2 << 10
)

// ResponseField is one member a batch's rows may be limited to
// (CreateBatchRequest.ResponseFields). The SDK accepts only the documented
// fields, because a typo would otherwise cost a paid batch.
type ResponseField string

// Documented response fields: every Verification member but duration, which
// only single verifications report.
const (
	ResponseFieldAcceptAll    ResponseField = "accept_all"
	ResponseFieldBirthYear    ResponseField = "birth_year"
	ResponseFieldDidYouMean   ResponseField = "did_you_mean"
	ResponseFieldDisposable   ResponseField = "disposable"
	ResponseFieldDomain       ResponseField = "domain"
	ResponseFieldEmail        ResponseField = "email"
	ResponseFieldFirstName    ResponseField = "first_name"
	ResponseFieldFree         ResponseField = "free"
	ResponseFieldFullName     ResponseField = "full_name"
	ResponseFieldGender       ResponseField = "gender"
	ResponseFieldLastName     ResponseField = "last_name"
	ResponseFieldMailboxFull  ResponseField = "mailbox_full"
	ResponseFieldMXRecord     ResponseField = "mx_record"
	ResponseFieldNoReply      ResponseField = "no_reply"
	ResponseFieldReason       ResponseField = "reason"
	ResponseFieldRole         ResponseField = "role"
	ResponseFieldScore        ResponseField = "score"
	ResponseFieldSMTPProvider ResponseField = "smtp_provider"
	ResponseFieldState        ResponseField = "state"
	ResponseFieldTag          ResponseField = "tag"
	ResponseFieldUser         ResponseField = "user"
)

// Known reports whether f is one of the 21 documented response fields.
func (f ResponseField) Known() bool {
	switch f {
	case ResponseFieldAcceptAll, ResponseFieldBirthYear, ResponseFieldDidYouMean, ResponseFieldDisposable,
		ResponseFieldDomain, ResponseFieldEmail, ResponseFieldFirstName, ResponseFieldFree,
		ResponseFieldFullName, ResponseFieldGender, ResponseFieldLastName, ResponseFieldMailboxFull,
		ResponseFieldMXRecord, ResponseFieldNoReply, ResponseFieldReason, ResponseFieldRole,
		ResponseFieldScore, ResponseFieldSMTPProvider, ResponseFieldState, ResponseFieldTag,
		ResponseFieldUser:
		return true
	}
	return false
}

// Simulation asks the provider to simulate an outcome instead of acting
// ("simulate"). The provider documents it for test keys; its effect with a
// live key is undocumented. CreateBatch accepts SimulateGenericError,
// SimulateInsufficientCreditsError and SimulatePaymentError; GetBatch accepts
// SimulateGenericError, SimulateImporting, SimulateVerifying and
// SimulatePaused.
type Simulation string

// Documented simulations.
const (
	// SimulateGenericError simulates a generic failure (create and get).
	SimulateGenericError Simulation = "generic_error"

	// SimulateInsufficientCreditsError simulates an account without enough
	// credits (create).
	SimulateInsufficientCreditsError Simulation = "insufficient_credits_error"

	// SimulatePaymentError simulates a failed auto-refill payment (create).
	SimulatePaymentError Simulation = "payment_error"

	// SimulateImporting simulates a batch still being imported (get).
	SimulateImporting Simulation = "importing"

	// SimulateVerifying simulates a batch still being verified (get).
	SimulateVerifying Simulation = "verifying"

	// SimulatePaused simulates a paused batch (get).
	SimulatePaused Simulation = "paused"
)

// Known reports whether s is one of the six documented simulations.
func (s Simulation) Known() bool {
	return s.forCreate() || s.forGet()
}

// forCreate reports whether CreateBatch accepts s.
func (s Simulation) forCreate() bool {
	return s == SimulateGenericError || s == SimulateInsufficientCreditsError || s == SimulatePaymentError
}

// forGet reports whether GetBatch accepts s.
func (s Simulation) forGet() bool {
	return s == SimulateGenericError || s == SimulateImporting || s == SimulateVerifying || s == SimulatePaused
}

// checkSimulation validates an optional Simulate value against the
// simulations an operation accepts: "" is not sent, an undocumented value is
// unknown_value, and a value for the other operation is wrong_operation.
func checkSimulation(s Simulation, accepts func(Simulation) bool) error {
	switch {
	case s == "":
		return nil
	case !s.Known():
		return invalidRequest("Simulate", reasonUnknownValue)
	case !accepts(s):
		return invalidRequest("Simulate", reasonWrongOperation)
	}
	return nil
}

// CreateBatchRequest is one POST /v1/batch call. It is read only while
// CreateBatch encodes it, before any I/O, and is never kept or modified;
// changing its slices while the call runs is a data race.
type CreateBatchRequest struct {
	// Emails are the addresses to verify: 1 to the Client's limit
	// (DefaultMaxBatchEmails unless raised with WithMaxBatchEmails), sent as
	// a JSON array in the given order, duplicates included. Each follows the
	// VerifyEmailRequest.Email rules and must not contain a comma, which the
	// provider treats as a separator: an embedded comma could turn one
	// address into several billable rows. The whole encoded body must fit
	// 64 MiB.
	Emails []string

	// CallbackURL, when set, is sent as "url": the provider POSTs the batch's
	// results there when it completes (see DecodeBatchCallback). It must be
	// an absolute https URL with a host and no userinfo or fragment, at most
	// 2048 bytes of valid UTF-8 without whitespace or control characters.
	// The provider documents no signature on callbacks: put a secret token
	// of your own in its query, and check it before trusting a callback. The
	// SDK never prints it.
	CallbackURL string

	// ResponseFields, when set, limits each row to these members, sent
	// comma-joined in the given order. Each must be Known, without
	// duplicates. Leaving out ResponseFieldEmail is allowed, but row order is
	// undocumented, so rows could then not be matched to addresses.
	ResponseFields []ResponseField

	// Retries, when non-nil, is sent as "retries": false turns off the
	// provider's own retries of inconclusive verifications, which improve
	// accuracy. nil is not sent (the provider's default is true). These are
	// never SDK retries.
	Retries *bool

	// Simulate, when set, is sent as "simulate": one of the simulations
	// CreateBatch accepts (see Simulation), for test keys.
	Simulate Simulation
}

// String describes the request's shape only, such as
// "emailable.CreateBatchRequest{emails: 3, callbackURL: set, responseFields:
// 2, retries: unset, simulate: unset}": counts and set or unset, never the
// addresses or the callback URL.
func (r CreateBatchRequest) String() string {
	return describe("emailable.CreateBatchRequest",
		field{"emails", strconv.Itoa(len(r.Emails))},
		field{"callbackURL", setOrUnset(r.CallbackURL != "")},
		field{"responseFields", strconv.Itoa(len(r.ResponseFields))},
		field{"retries", boolOrUnset(r.Retries)},
		field{"simulate", setOrUnset(r.Simulate != "")},
	)
}

// GoString returns the same text as String, so that %#v prints the shape only.
func (r CreateBatchRequest) GoString() string { return r.String() }

// Format prints the same text as String for every fmt verb (quoted for %q),
// so that no verb reaches the addresses or the callback URL by reflection.
// fmt cannot call it for %p on a value, nor for a request held in an
// unexported struct field: keep requests out of formatted output there.
func (r CreateBatchRequest) Format(f fmt.State, verb rune) { formatAs(f, verb, r.String()) }

// CreateBatchResult is the provider's answer to POST /v1/batch.
type CreateBatchResult struct {
	// BatchID is the new batch's ID. Persist it before anything else: it is
	// the only way to read the batch.
	BatchID string `json:"id"`

	// Message is the provider's text, never interpreted. It is "" when the
	// provider sent none, or sent one the SDK could not read.
	Message string `json:"message,omitempty"`
}

// CreateBatch submits addresses for asynchronous verification: POST
// https://api.emailable.com/v1/batch.
//
// STATE-CHANGING and PAID: an answer means the provider accepted the batch,
// not that it is verified, and verification, which uses credits, starts at
// once. The provider documents no idempotency key. It makes exactly one
// attempt and never retries or resubmits:
//   - a 4xx means the provider refused the batch, so none should exist;
//   - an error matching ErrOutcomeUnknown means a batch may exist anyway:
//     reconcile by hand, for example in the provider's dashboard, and never
//     resubmit automatically;
//   - an error with a BatchID (a malformed_response: an answer the SDK could
//     not fully read but whose ID it could) means the batch exists: persist
//     the ID. It does not match ErrOutcomeUnknown.
//
// HTTP 402 can mean that the account's auto-refill needs a card charge. It
// needs a private key.
//
// Errors are a *ValidationError (nothing sent) or a *CallError.
func (c *Client) CreateBatch(ctx context.Context, req CreateBatchRequest) (*CreateBatchResult, error) {
	if err := c.prepare(ctx); err != nil {
		return nil, err
	}
	r, err := req.request(c)
	if err != nil {
		return nil, err
	}
	body, cerr := c.call(ctx, r)
	if cerr != nil {
		if body != nil {
			// The body was received but is not strict JSON: the batch
			// exists if it still names exactly one well-formed ID. An ID
			// that holds the API key or the callback URL, is a piece of the
			// key, or appears inside a request value (an address, the
			// callback URL or its token), which an echoing server could
			// send, is never kept: GoString prints BatchID.
			if id, ok := rescueBatchID(body); ok && !c.overlapsKey(id) && c.scrub(id, r.secrets) == id &&
				!req.echoes(id) {
				cerr.Kind, cerr.BatchID, cerr.OutcomeUnknown = FailureMalformedResponse, id, false
			}
		}
		return nil, cerr
	}
	res, derr := decodeCreateBatch(body)
	if derr != nil {
		return nil, r.decodeFailure(derr)
	}
	return res, nil
}

// echoes reports whether id appears inside any value of r, as an echoing
// server could send it back: an address, the callback URL or its token.
func (r CreateBatchRequest) echoes(id string) bool {
	return strings.Contains(r.CallbackURL, id) ||
		slices.ContainsFunc(r.Emails, func(e string) bool { return strings.Contains(e, id) })
}

// createBatchBody is the JSON body of POST /v1/batch, in member order.
type createBatchBody struct {
	Emails         []string `json:"emails"`
	URL            string   `json:"url,omitempty"`
	ResponseFields string   `json:"response_fields,omitempty"`
	Retries        *bool    `json:"retries,omitempty"`
	Simulate       string   `json:"simulate,omitempty"`
}

// request validates r and encodes it as the POST /v1/batch request:
// {"emails":[…][,"url":…][,"response_fields":"a,b"][,"retries":…]
// [,"simulate":…]}. The whole snapshot is validated and its size bounded
// before it is encoded, and nothing is streamed: validation always ends
// before the first byte is sent.
func (r CreateBatchRequest) request(c *Client) (*request, error) {
	size, err := r.validate(c.maxBatchEmails)
	if err != nil {
		return nil, err
	}
	if size > maxBatchRequestBytes {
		return nil, invalidRequest("request", reasonTooLarge)
	}
	fields := make([]string, len(r.ResponseFields))
	for i, f := range r.ResponseFields {
		fields[i] = string(f)
	}
	var buf bytes.Buffer
	buf.Grow(size + 1)
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(createBatchBody{
		Emails:         r.Emails,
		URL:            r.CallbackURL,
		ResponseFields: strings.Join(fields, ","),
		Retries:        r.Retries,
		Simulate:       string(r.Simulate),
	}); err != nil {
		return nil, invalidRequest("request", reasonInvalidFormat)
	}
	var secrets []string
	if r.CallbackURL != "" {
		secrets = callbackSecrets(r.CallbackURL)
	}
	return &request{
		op:      OpCreateBatch,
		method:  http.MethodPost,
		path:    "batch",
		body:    bytes.TrimSuffix(buf.Bytes(), []byte("\n")),
		paid:    true,
		secrets: secrets,
	}, nil
}

// validate checks every field and returns the exact size of the encoded body.
func (r CreateBatchRequest) validate(maxEmails int) (int, error) {
	switch n := len(r.Emails); {
	case n == 0:
		return 0, invalidRequest("Emails", reasonRequired)
	case n > maxEmails:
		return 0, invalidRequest("Emails", reasonTooMany)
	}
	size := len(`{"emails":[]}`) + len(r.Emails) - 1
	for i, e := range r.Emails {
		reason := emailProblem(e)
		if reason == "" && strings.IndexByte(e, ',') >= 0 {
			reason = reasonContainsComma
		}
		if reason != "" {
			return 0, invalidRequest("Emails["+strconv.Itoa(i)+"]", reason)
		}
		size += encodedStringSize(e)
	}
	if r.CallbackURL != "" {
		if err := checkCallbackURL(r.CallbackURL); err != nil {
			return 0, err
		}
		size += len(`,"url":`) + encodedStringSize(r.CallbackURL)
	}
	if n, err := checkResponseFields(r.ResponseFields); err != nil {
		return 0, err
	} else if n > 0 {
		size += len(`,"response_fields":""`) + n
	}
	if r.Retries != nil {
		size += len(`,"retries":`) + len(strconv.FormatBool(*r.Retries))
	}
	if err := checkSimulation(r.Simulate, Simulation.forCreate); err != nil {
		return 0, err
	}
	if r.Simulate != "" {
		size += len(`,"simulate":""`) + len(r.Simulate)
	}
	return size, nil
}

// checkResponseFields validates the response fields and returns the length of
// their comma-joined form, 0 when there are none.
func checkResponseFields(fields []ResponseField) (int, error) {
	n := 0
	for i, f := range fields {
		field := "ResponseFields[" + strconv.Itoa(i) + "]"
		if !f.Known() {
			return 0, invalidRequest(field, reasonUnknownValue)
		}
		if slices.Contains(fields[:i], f) {
			return 0, invalidRequest(field, reasonDuplicate)
		}
		if i > 0 {
			n++
		}
		n += len(f)
	}
	return n, nil
}

// encodedStringSize returns the length of s once encoded as a JSON string by
// an encoder that does not escape HTML: s is valid UTF-8 without control
// characters, which validation has checked, so only '"', '\' and the line and
// paragraph separators U+2028 and U+2029 grow.
func encodedStringSize(s string) int {
	n := len(s) + 2
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			n++
			i++
		case c < utf8.RuneSelf:
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == '\u2028' || r == '\u2029' {
				n += 6 - size
			}
			i += size
		}
	}
	return n
}

// callbackSecrets returns the secrets a callback URL holds, for scrubbing
// provider text: the URL itself, and each value of its query of at least
// minKeyOverlap bytes, such as a token of your own.
func callbackSecrets(raw string) []string {
	out := []string{raw}
	if u, err := url.Parse(raw); err == nil {
		for _, vs := range u.Query() {
			for _, v := range vs {
				if len(v) >= minKeyOverlap {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// checkCallbackURL validates CallbackURL: an absolute https URL with a host,
// no userinfo and no fragment, without whitespace or control characters. The
// error never quotes it.
func checkCallbackURL(raw string) error {
	const field = "CallbackURL"
	if err := checkOptionalText(field, raw, maxCallbackURLBytes); err != nil {
		return err
	}
	if strings.ContainsFunc(raw, unicode.IsSpace) {
		return invalidRequest(field, reasonInvalidURL)
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil || strings.Contains(raw, "#"):
		return invalidRequest(field, reasonInvalidURL)
	case !strings.EqualFold(u.Scheme, "https"):
		return invalidRequest(field, reasonUnsupportedScheme)
	}
	return nil
}

// decodeCreateBatch decodes a POST /v1/batch answer: a required id, and a
// message that is dropped, never failing the call, when it is not readable
// text.
func decodeCreateBatch(body []byte) (*CreateBatchResult, *decodeError) {
	o := readObject(body, []string{"id", "message"})
	if isNull(o.fields[0]) {
		return nil, missingField("id")
	}
	id, derr := decodeBatchID(o.fields[0])
	if derr != nil {
		return nil, derr.at("id")
	}
	message, derr := decodeText(o.fields[1], maxTextBytes)
	if derr != nil {
		message = ""
	}
	return &CreateBatchResult{BatchID: id, Message: message}, nil
}

// rescueBatchID returns the batch ID of an answer that is not strict JSON,
// when it can still be read without doubt: the body's first value is a JSON
// object with exactly one key that folds to "id", spelled exactly "id", whose
// value is a well-formed batch ID. Invalid UTF-8 elsewhere in the body does
// not matter: a well-formed ID is visible ASCII.
func rescueBatchID(body []byte) (string, bool) {
	first, ok := firstValue(body)
	if !ok || first[0] != '{' {
		return "", false
	}
	var raw []byte
	n := 0
	for key, value := range members(first) {
		if !foldsToID(key) {
			continue
		}
		n++
		if string(keyBytes(key)) == "id" {
			raw = value
		}
	}
	if n != 1 || !isString(raw) {
		return "", false
	}
	id, derr := decodeBatchID(raw)
	if derr != nil {
		return "", false
	}
	return id, true
}

// foldsToID reports whether the key literal lit folds to "id" as
// encoding/json folds keys: "id", "ID", "Id" and any escaped spelling.
func foldsToID(lit []byte) bool {
	return string(appendFolded(nil, lit)) == "ID"
}
