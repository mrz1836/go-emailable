package emailable_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Characters encoding/json escapes or rewrites: U+2028 LINE SEPARATOR, which
// it escapes, and the replacement character a lone surrogate escape becomes.
const (
	roundTripLineSep  = string(rune(0x2028))
	roundTripReplaced = string(rune(0xFFFD))
)

// roundTripDownload is a signed download URL a batch snapshot carries.
const roundTripDownload = "https://files.example.com/batch/roundtrip.csv?signature=rtsig"

// roundTripGetPartial makes one partial GetBatch for testBatchID.
func roundTripGetPartial(ctx context.Context, c *emailable.Client) (*emailable.Batch, error) {
	return c.GetBatch(ctx, emailable.GetBatchRequest{BatchID: testBatchID, Partial: true})
}

// roundTripEveryField returns a Verification with every field set to a value
// other than its zero, text that encoding/json escapes included.
func roundTripEveryField() *emailable.Verification {
	return &emailable.Verification{
		Email:        testEmail,
		State:        emailable.StateRisky,
		Reason:       emailable.ReasonLowDeliverability,
		Score:        new(42),
		AcceptAll:    new(true),
		Disposable:   new(true),
		Free:         new(true),
		MailboxFull:  new(true),
		NoReply:      new(true),
		Role:         new(true),
		BirthYear:    new(1987),
		DidYouMean:   new("jane.doe@mail.example.com"),
		Domain:       new("example.com"),
		User:         new("jane.doe"),
		Tag:          new("news"),
		MXRecord:     new("mx.example.com"),
		SMTPProvider: new("Example <Mail> & co" + roundTripLineSep),
		FirstName:    new("Jane"),
		LastName:     new("Doe"),
		FullName:     new("Jane Doe"),
		Gender:       new("female"),
		Duration:     new(0.484),
	}
}

// roundTripZeroPointers returns a Verification whose every pointer field
// points to its zero value: reported as false, 0 or "", never omitted.
func roundTripZeroPointers() *emailable.Verification {
	return &emailable.Verification{
		Score:        new(0),
		AcceptAll:    new(false),
		Disposable:   new(false),
		Free:         new(false),
		MailboxFull:  new(false),
		NoReply:      new(false),
		Role:         new(false),
		BirthYear:    new(0),
		DidYouMean:   new(""),
		Domain:       new(""),
		User:         new(""),
		Tag:          new(""),
		MXRecord:     new(""),
		SMTPProvider: new(""),
		FirstName:    new(""),
		LastName:     new(""),
		FullName:     new(""),
		Gender:       new(""),
		Duration:     new(0.0),
	}
}

func TestRoundTripVerificationValues(t *testing.T) {
	tests := []struct {
		name string
		v    *emailable.Verification
		want string
	}{
		{
			name: "every field",
			v:    roundTripEveryField(),
			want: testutil.Esc(`{"email":"jane.doe@example.com","state":"risky","reason":"low_deliverability",` +
				`"score":42,"accept_all":true,"disposable":true,"free":true,"mailbox_full":true,"no_reply":true,` +
				`"role":true,"birth_year":1987,"did_you_mean":"jane.doe@mail.example.com","domain":"example.com",` +
				`"user":"jane.doe","tag":"news","mx_record":"mx.example.com",` +
				`"smtp_provider":"Example ~u003cMail~u003e ~u0026 co~u2028","first_name":"Jane","last_name":"Doe",` +
				`"full_name":"Jane Doe","gender":"female","duration":0.484}`),
		},
		{
			name: "every pointer to its zero",
			v:    roundTripZeroPointers(),
			want: `{"score":0,"accept_all":false,"disposable":false,"free":false,"mailbox_full":false,` +
				`"no_reply":false,"role":false,"birth_year":0,"did_you_mean":"","domain":"","user":"","tag":"",` +
				`"mx_record":"","smtp_provider":"","first_name":"","last_name":"","full_name":"","gender":"",` +
				`"duration":0}`,
		},
		{
			name: "all nil",
			v:    &emailable.Verification{},
			want: `{}`,
		},
		{
			name: "mixed",
			v: &emailable.Verification{
				Email: testEmail, State: emailable.StateDeliverable, Score: new(0), Free: new(false), Role: new(true),
				Tag: new(""), Gender: new("male"), Duration: new(1.5),
			},
			want: `{"email":"jane.doe@example.com","state":"deliverable","score":0,"free":false,"role":true,` +
				`"tag":"","gender":"male","duration":1.5}`,
		},
		{
			name: "undocumented state and reason",
			v:    &emailable.Verification{Email: testEmail, State: "future_state", Reason: "future_reason"},
			want: `{"email":"jane.doe@example.com","state":"future_state","reason":"future_reason"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			back := assertRoundTrip(t, tt.v, tt.want)
			assert.Equal(t, tt.v, back)
		})
	}
}

func TestRoundTripVerificationKeepsReportedZeros(t *testing.T) {
	back := assertRoundTrip(t, roundTripZeroPointers(), `{"score":0,"accept_all":false,"disposable":false,`+
		`"free":false,"mailbox_full":false,"no_reply":false,"role":false,"birth_year":0,"did_you_mean":"",`+
		`"domain":"","user":"","tag":"","mx_record":"","smtp_provider":"","first_name":"","last_name":"",`+
		`"full_name":"","gender":"","duration":0}`)
	for name, p := range map[string]*bool{
		"accept_all": back.AcceptAll, "disposable": back.Disposable, "free": back.Free,
		"mailbox_full": back.MailboxFull, "no_reply": back.NoReply, "role": back.Role,
	} {
		require.NotNil(t, p, "%s: a reported false is &false, never nil", name)
		assert.False(t, *p, name)
	}
	require.NotNil(t, back.Score)
	assert.Zero(t, *back.Score)
	require.NotNil(t, back.Tag, `a reported "" is &"", never nil`)
	assert.Empty(t, *back.Tag)
	require.NotNil(t, back.Duration)
	assert.Zero(t, *back.Duration)
}

func TestRoundTripVerificationFromProvider(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "documented answer",
			body: `{"accept_all":false,"did_you_mean":null,"disposable":false,"domain":"example.com",` +
				`"duration":0.493,"email":"jane.doe@example.com","first_name":"Jane","free":false,` +
				`"full_name":"Jane Doe","gender":"female","last_name":"Doe","mailbox_full":false,` +
				`"mx_record":"mx.example.com","no_reply":false,"reason":"accepted_email","role":false,` +
				`"score":100,"smtp_provider":"google","state":"deliverable","tag":null,"user":"jane.doe"}`,
			want: `{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email","score":100,` +
				`"accept_all":false,"disposable":false,"free":false,"mailbox_full":false,"no_reply":false,` +
				`"role":false,"domain":"example.com","user":"jane.doe","mx_record":"mx.example.com",` +
				`"smtp_provider":"google","first_name":"Jane","last_name":"Doe","full_name":"Jane Doe",` +
				`"gender":"female","duration":0.493}`,
		},
		{
			name: "smallest answer",
			body: verifyOK,
			want: `{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email"}`,
		},
		{
			name: "unknown state with nulls, unknown members and escapes",
			body: testutil.Esc(`{"email":"jane.doe@example.com","state":"unknown","reason":"no_connect",` +
				`"score":null,"free":null,"role":true,"birth_year":0,"tag":"","first_name":"J~u0061ne",` +
				`"gender":"a~ud800b","smtp_provider":"x<y>&z` + roundTripLineSep + `","duration":-0,` +
				`"future_member":{"nested":[1,2,{"k":null}]}}`),
			want: testutil.Esc(`{"email":"jane.doe@example.com","state":"unknown","reason":"no_connect",` +
				`"role":true,"birth_year":0,"tag":"","smtp_provider":"x~u003cy~u003e~u0026z~u2028",` +
				`"first_name":"Jane","gender":"a` + roundTripReplaced + `b","duration":0}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := callOnce(t, pathVerify, tt.body, callVerify)
			require.NoError(t, err)
			back := assertRoundTrip(t, v, tt.want)
			assert.Equal(t, v, back)
		})
	}
}

// roundTripWithoutUnserialized returns a copy of b without the members that
// are never serialized: DownloadFile and Partial.
func roundTripWithoutUnserialized(b *emailable.Batch) *emailable.Batch {
	c := *b
	c.DownloadFile = emailable.DownloadURL{}
	c.Partial = false
	return &c
}

func TestRoundTripBatchSnapshot(t *testing.T) {
	body := `{"message":"Batch verification completed.","id":"` + testBatchID + `","processed":2,"total":2,` +
		`"emails":[{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email","score":95,` +
		`"free":false,"tag":""},{"email":"blake@mail.example.com","state":"undeliverable","reason":"rejected_email",` +
		`"score":0}],"download_file":"` + roundTripDownload + `",` +
		`"reason_counts":{"rejected_email":1,"accepted_email":1},` +
		`"total_counts":{"undeliverable":1,"deliverable":1,"duplicate":0,"processed":2,"total":2}}`
	want := `{"id":"` + testBatchID + `","message":"Batch verification completed.","processed":2,"total":2,` +
		`"emails":[{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email","score":95,` +
		`"free":false,"tag":""},{"email":"blake@mail.example.com","state":"undeliverable","reason":"rejected_email",` +
		`"score":0}],"reason_counts":{"accepted_email":1,"rejected_email":1},` +
		`"total_counts":{"deliverable":1,"duplicate":0,"processed":2,"total":2,"undeliverable":1}}`

	b, err := callOnce(t, pathBatch, body, roundTripGetPartial)
	require.NoError(t, err)
	require.True(t, b.Partial)
	require.Equal(t, roundTripDownload, b.DownloadFile.Reveal())

	data, err := json.Marshal(b)
	require.NoError(t, err)
	assertBody(t, want, data)
	assert.NotContains(t, string(data), "download", "DownloadFile is never serialized")
	assert.NotContains(t, string(data), "partial", "Partial is never serialized")
	assert.NotContains(t, string(data), "rtsig")

	var back emailable.Batch
	require.NoError(t, json.Unmarshal(data, &back))
	assert.True(t, back.DownloadFile.IsZero(), "a stored snapshot has no download URL")
	assert.False(t, back.Partial)
	assert.Equal(t, roundTripWithoutUnserialized(b), &back, "everything else survives")
}

func TestRoundTripBatchNilAndEmpty(t *testing.T) {
	id := `"id":"` + testBatchID + `"`
	tests := []struct {
		name  string
		body  string
		want  string
		check func(t *testing.T, b *emailable.Batch)
	}{
		{
			name: "rows present and empty",
			body: `{` + id + `,"emails":[]}`,
			want: `{` + id + `,"emails":[]}`,
			check: func(t *testing.T, b *emailable.Batch) {
				t.Helper()
				assert.NotNil(t, b.Emails, "an empty list is kept: omitzero keeps []")
				assert.Empty(t, b.Emails)
			},
		},
		{
			name: "every optional member null",
			body: `{` + id + `,"message":null,"processed":null,"total":null,"emails":null,"download_file":null,` +
				`"reason_counts":null,"total_counts":null}`,
			want: `{` + id + `}`,
			check: func(t *testing.T, b *emailable.Batch) {
				t.Helper()
				assert.Nil(t, b.Emails)
				assert.Nil(t, b.ReasonCounts)
				assert.Nil(t, b.TotalCounts)
				assert.Nil(t, b.Processed)
				assert.Nil(t, b.Total)
			},
		},
		{
			name: "counts present and empty",
			body: `{` + id + `,"reason_counts":{},"total_counts":{}}`,
			want: `{` + id + `,"reason_counts":{},"total_counts":{}}`,
			check: func(t *testing.T, b *emailable.Batch) {
				t.Helper()
				assert.NotNil(t, b.ReasonCounts, "an empty map is kept: omitzero keeps {}")
				assert.NotNil(t, b.TotalCounts)
				assert.Empty(t, b.ReasonCounts)
				assert.Empty(t, b.TotalCounts)
			},
		},
		{
			name: "progress without an id",
			body: `{"message":"Your batch is being processed.","processed":5,"total":10}`,
			want: `{` + id + `,"message":"Your batch is being processed.","processed":5,"total":10}`,
			check: func(t *testing.T, b *emailable.Batch) {
				t.Helper()
				processed, total, ok := b.Progress()
				assert.True(t, ok)
				assert.Equal(t, int64(5), processed)
				assert.Equal(t, int64(10), total)
			},
		},
		{
			name: "counts alone after retention, with zero counts",
			body: `{` + id + `,"total_counts":{"processed":0,"total":0,"future_total":3}}`,
			want: `{` + id + `,"total_counts":{"future_total":3,"processed":0,"total":0}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := callOnce(t, pathBatch, tt.body, callGet)
			require.NoError(t, err)
			back := assertRoundTrip(t, b, tt.want)
			if tt.check != nil {
				tt.check(t, b)
				tt.check(t, back)
			}
		})
	}
}

func TestRoundTripBatchValues(t *testing.T) {
	tests := []struct {
		name string
		b    *emailable.Batch
		want string
	}{
		{"zero", &emailable.Batch{}, `{"id":""}`},
		{"nil and empty differ", &emailable.Batch{
			ID: testBatchID, Emails: []emailable.Verification{}, ReasonCounts: map[emailable.Reason]int64{},
		}, `{"id":"` + testBatchID + `","emails":[],"reason_counts":{}}`},
		{"zero counts kept", &emailable.Batch{
			ID: testBatchID, Processed: new(int64(0)), Total: new(int64(0)),
			Emails:       []emailable.Verification{{}, {Email: testEmail, Score: new(0)}},
			TotalCounts:  map[string]int64{"total": 0},
			ReasonCounts: map[emailable.Reason]int64{emailable.ReasonTimeout: 0, "future_reason": 7},
		}, `{"id":"` + testBatchID + `","processed":0,"total":0,"emails":[{},{"email":"jane.doe@example.com",` +
			`"score":0}],"reason_counts":{"future_reason":7,"timeout":0},"total_counts":{"total":0}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRoundTrip(t, tt.b, tt.want)
		})
	}
}

func TestRoundTripCreateBatchResult(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"with message", createOK, `{"id":"` + testBatchID + `","message":"Batch successfully created."}`},
		{"without message", `{"id":"` + testBatchID + `"}`, `{"id":"` + testBatchID + `"}`},
		{"unreadable message dropped", `{"id":"` + testBatchID + `","message":12}`, `{"id":"` + testBatchID + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := callOnce(t, pathBatch, tt.body, callCreate)
			require.NoError(t, err)
			assertRoundTrip(t, res, tt.want)
		})
	}
	assertRoundTrip(t, &emailable.CreateBatchResult{}, `{"id":""}`)
}

func TestRoundTripAccountInfo(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"documented answer", accountOK, `{"owner_email":"` + testOwner + `","available_credits":1000}`},
		{
			"no credits", `{"available_credits":0,"owner_email":"` + testOwner + `"}`,
			`{"owner_email":"` + testOwner + `","available_credits":0}`,
		},
		{
			"empty owner", `{"owner_email":"","available_credits":9223372036854775807}`,
			`{"owner_email":"","available_credits":9223372036854775807}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := callOnce(t, pathAccount, tt.body, callAccount)
			require.NoError(t, err)
			assertRoundTrip(t, info, tt.want)
		})
	}
	assertRoundTrip(t, &emailable.AccountInfo{}, `{"owner_email":"","available_credits":0}`)
}

func TestRoundTripTokensMarshalAsStrings(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
		back  func([]byte) (any, error)
	}{
		{"state", emailable.StateDeliverable, `"deliverable"`, roundTripUnmarshal[emailable.State]},
		{"undocumented state", emailable.State("future_state"), `"future_state"`, roundTripUnmarshal[emailable.State]},
		{"reason", emailable.ReasonAcceptedEmail, `"accepted_email"`, roundTripUnmarshal[emailable.Reason]},
		{"response field", emailable.ResponseFieldSMTPProvider, `"smtp_provider"`, roundTripUnmarshal[emailable.ResponseField]},
		{
			"simulation", emailable.SimulateInsufficientCreditsError, `"insufficient_credits_error"`,
			roundTripUnmarshal[emailable.Simulation],
		},
		{"failure kind", emailable.FailureBudgetExhausted, `"budget_exhausted"`, roundTripUnmarshal[emailable.FailureKind]},
		{"operation", emailable.OpBatchCallback, `"batch_callback"`, roundTripUnmarshal[emailable.Operation]},
		{
			"states in a list",
			[]emailable.State{emailable.StateRisky, emailable.StateDuplicate},
			`["risky","duplicate"]`,
			roundTripUnmarshal[[]emailable.State],
		},
		{
			"reasons as map keys",
			map[emailable.Reason]int64{emailable.ReasonInvalidSMTP: 1, emailable.ReasonNoConnect: 2},
			`{"invalid_smtp":1,"no_connect":2}`, roundTripUnmarshal[map[emailable.Reason]int64],
		},
		{
			"response fields in a list",
			[]emailable.ResponseField{emailable.ResponseFieldEmail, emailable.ResponseFieldState},
			`["email","state"]`, roundTripUnmarshal[[]emailable.ResponseField],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.value)
			require.NoError(t, err)
			assertBody(t, tt.want, data)
			back, err := tt.back(data)
			require.NoError(t, err)
			assert.Equal(t, tt.value, back)
		})
	}
}

// roundTripUnmarshal decodes data into a new T with encoding/json.
func roundTripUnmarshal[T any](data []byte) (any, error) {
	var v T
	err := json.Unmarshal(data, &v)
	return v, err
}

// TestRoundTripSDKAgreesWithEncodingJSON decodes valid provider bodies both
// with the SDK and with encoding/json into the same type: they agree, apart
// from what the SDK documents it adds. It reads download_file into
// DownloadFile, which encoding/json never fills (json:"-"), and gives a
// GetBatch snapshot the requested ID.
func TestRoundTripSDKAgreesWithEncodingJSON(t *testing.T) {
	t.Run("verification", func(t *testing.T) {
		bodies := []string{
			verifyOK,
			`{"email":"jane.doe@example.com","state":"risky","reason":"low_quality","score":40,"accept_all":true,` +
				`"disposable":false,"free":true,"mailbox_full":false,"no_reply":false,"role":true,"birth_year":1990,` +
				`"did_you_mean":"jane.doe@mail.example.com","domain":"example.com","user":"jane.doe","tag":"x",` +
				`"mx_record":"mx.example.com","smtp_provider":"google","first_name":"Jane","last_name":"Doe",` +
				`"full_name":"Jane Doe","gender":"female","duration":0.21}`,
			testutil.Esc(`{"email":"jane~u002edoe@example.com","state":"unknown","tag":null,"user":"~ud83d~ude00",` +
				`"domain":"a~ud800b","score":0,"future":[{"x":1}]}`),
		}
		for _, body := range bodies {
			v, err := callOnce(t, pathVerify, body, callVerify)
			require.NoError(t, err)
			var direct emailable.Verification
			require.NoError(t, json.Unmarshal([]byte(body), &direct))
			assert.Equal(t, &direct, v)
		}
	})
	t.Run("batch", func(t *testing.T) {
		bodies := []string{
			batchOK,
			`{"id":"` + testBatchID + `","message":"done","processed":3,"total":3,"emails":[{"email":"a@example.com",` +
				`"state":"duplicate"},{}],"reason_counts":{"accepted_email":2},"total_counts":{"duplicate":1}}`,
			`{"id":"` + testBatchID + `","download_file":"` + roundTripDownload + `","total_counts":{"total":9}}`,
		}
		for _, body := range bodies {
			b, err := callOnce(t, pathBatch, body, callGet)
			require.NoError(t, err)
			var direct emailable.Batch
			require.NoError(t, json.Unmarshal([]byte(body), &direct))
			assert.True(t, direct.DownloadFile.IsZero(), "encoding/json never reads download_file")
			assert.Equal(t, &direct, roundTripWithoutUnserialized(b))
		}
	})
	t.Run("create batch result", func(t *testing.T) {
		res, err := callOnce(t, pathBatch, createOK, callCreate)
		require.NoError(t, err)
		var direct emailable.CreateBatchResult
		require.NoError(t, json.Unmarshal([]byte(createOK), &direct))
		assert.Equal(t, &direct, res)
	})
	t.Run("account info", func(t *testing.T) {
		info, err := callOnce(t, pathAccount, accountOK, callAccount)
		require.NoError(t, err)
		var direct emailable.AccountInfo
		require.NoError(t, json.Unmarshal([]byte(accountOK), &direct))
		assert.Equal(t, &direct, info)
	})
}

// roundTripRaw is a body that encoding/json accepts into a result type and
// the SDK refuses.
type roundTripRaw struct {
	name   string
	path   string
	body   string
	direct func(t *testing.T, body []byte) // decodes body with encoding/json and checks what it kept
	call   func(ctx context.Context, c *emailable.Client) error
	want   wantCall
}

// roundTripDirect returns a check that decodes body into a new T with
// encoding/json, which must succeed, and asserts that got finds want in it.
func roundTripDirect[T, V any](got func(*T) V, want V) func(t *testing.T, body []byte) {
	return func(t *testing.T, body []byte) {
		t.Helper()
		v := new(T)
		require.NoError(t, json.Unmarshal(body, v), "encoding/json accepts the body")
		assert.Equal(t, want, got(v))
	}
}

// roundTripRawCases returns the bodies of TestRoundTripRawUnmarshalSkipsChecks.
func roundTripRawCases() []roundTripRaw {
	verify := opCaseNamed("VerifyEmail")
	get := opCaseNamed("GetBatch")
	account := opCaseNamed("AccountInfo")
	malformed := func(o opCase, field string) wantCall {
		return o.want(wantCall{
			Kind: emailable.FailureMalformedResponse, Status: http.StatusOK, Attempts: 1, Field: field,
			OutcomeUnknown: o.paid,
		})
	}
	unexpected := func(o opCase, field string) wantCall {
		w := malformed(o, field)
		w.Kind = emailable.FailureUnexpectedResponse
		return w
	}
	e := `"email":"` + testEmail + `"`
	return []roundTripRaw{
		{
			"score out of range", pathVerify, `{` + e + `,"state":"deliverable","score":250}`,
			roundTripDirect(func(v *emailable.Verification) *int { return v.Score }, new(250)),
			verify.call, malformed(verify, "score"),
		},
		{
			"state not a token", pathVerify, `{` + e + `,"state":"Deliverable!"}`,
			roundTripDirect(func(v *emailable.Verification) emailable.State { return v.State }, "Deliverable!"),
			verify.call, unexpected(verify, "state"),
		},
		{
			"repeated key, the last one wins", pathVerify, `{` + e + `,"state":"deliverable","state":"undeliverable"}`,
			roundTripDirect(func(v *emailable.Verification) emailable.State { return v.State }, emailable.StateUndeliverable),
			verify.call, malformed(verify, ""),
		},
		{
			"case variant overrides", pathVerify, `{` + e + `,"state":"deliverable","STATE":"risky"}`,
			roundTripDirect(func(v *emailable.Verification) emailable.State { return v.State }, emailable.StateRisky),
			verify.call, malformed(verify, ""),
		},
		{
			"keys matched ignoring case", pathVerify, `{"EMAIL":"` + testEmail + `","State":"deliverable"}`,
			roundTripDirect(func(v *emailable.Verification) string { return v.Email }, testEmail),
			verify.call, malformed(verify, "email"),
		},
		{
			"invalid UTF-8 replaced", pathVerify, `{` + e + `,"state":"deliverable","user":"jane` + "\xff" + `"}`,
			roundTripDirect(func(v *emailable.Verification) *string { return v.User }, new("jane"+roundTripReplaced)),
			verify.call, malformed(verify, ""),
		},
		{
			"required email missing", pathVerify, `{"state":"deliverable"}`,
			roundTripDirect(func(v *emailable.Verification) string { return v.Email }, ""),
			verify.call, malformed(verify, "email"),
		},
		{
			"negative duration", pathVerify, `{` + e + `,"state":"deliverable","duration":-1.5}`,
			roundTripDirect(func(v *emailable.Verification) *float64 { return v.Duration }, new(-1.5)),
			verify.call, malformed(verify, "duration"),
		},
		{
			"another batch", pathBatch, `{"id":"other-batch","message":"done"}`,
			roundTripDirect(func(b *emailable.Batch) string { return b.ID }, "other-batch"),
			get.call, unexpected(get, "id"),
		},
		{
			"count key not a token", pathBatch, `{"id":"` + testBatchID + `","total_counts":{"Not A Token":1}}`,
			roundTripDirect(func(b *emailable.Batch) int64 { return b.TotalCounts["Not A Token"] }, 1),
			get.call, unexpected(get, "total_counts"),
		},
		{
			"negative credits", pathAccount, `{"owner_email":"` + testOwner + `","available_credits":-5}`,
			roundTripDirect(func(a *emailable.AccountInfo) int64 { return a.AvailableCredits }, -5),
			account.call, malformed(account, "available_credits"),
		},
	}
}

// TestRoundTripRawUnmarshalSkipsChecks documents that unmarshaling a raw
// provider body into a result type with encoding/json skips every check the
// SDK makes: it accepts what the SDK refuses, keeps the last of repeated
// keys, matches keys ignoring case and replaces invalid UTF-8.
func TestRoundTripRawUnmarshalSkipsChecks(t *testing.T) {
	for _, tt := range roundTripRawCases() {
		t.Run(tt.name, func(t *testing.T) {
			tt.direct(t, []byte(tt.body))
			c, s := newScriptClient(t, testutil.Step{Body: tt.body})
			err := tt.call(t.Context(), c)
			assert.Equal(t, []string{tt.path}, s.Paths())
			_ = assertCallError(t, err, tt.want)
		})
	}
}
