package emailable

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// GetBatchRequest is one GET /v1/batch call.
type GetBatchRequest struct {
	// BatchID is the batch to read: 1 to 256 bytes of visible ASCII (0x21 to
	// 0x7E), as CreateBatch returned it. It is sent in the URL query.
	BatchID string

	// Partial, when true, is sent as "partial=true": the provider then
	// includes the rows verified so far in an unfinished batch. It documents
	// this for batches of up to 1,000 addresses only.
	Partial bool

	// Simulate, when set, is sent as "simulate": one of the simulations
	// GetBatch accepts (see Simulation), for test keys.
	Simulate Simulation
}

// Batch is one snapshot of a batch: the answer to GetBatch, or the body of a
// completion callback (DecodeBatchCallback). Every part is optional and
// several can appear together, so read each on its own. The provider marks
// completion only in its free-text Message, which the SDK never interprets:
// deciding that a batch is complete is the caller's call.
//
// It holds contact data (the rows): treat it as personal data. It survives a
// JSON round trip through encoding/json unchanged, except DownloadFile and
// Partial, which are never serialized.
type Batch struct {
	// ID is the batch's ID: for GetBatch, the requested one (a response that
	// names another fails the call); for a callback, the body's.
	ID string `json:"id"`

	// Partial reports a snapshot read with GetBatchRequest.Partial, whose
	// Emails may be a subset of the batch. It is a snapshot, not a delta:
	// there is no cursor.
	Partial bool `json:"-"`

	// Message is the provider's text, never used to infer completion.
	Message string `json:"message,omitempty"`

	// Processed and Total are the top-level progress counts the provider
	// sends while a batch is processing. See Progress.
	Processed *int64 `json:"processed,omitempty"`
	Total     *int64 `json:"total,omitempty"`

	// Emails are the rows the provider sent inline, in its order: nil when
	// the member was absent, empty when it was present and empty. Every
	// field of a row is optional. Match rows to addresses by Email, never by
	// position: the provider documents neither the order of rows nor how
	// duplicates correlate, and does not promise one row per address.
	Emails []Verification `json:"emails,omitzero"`

	// DownloadFile is the signed URL of the batch's result file, which the
	// provider sends instead of inline rows for large batches. It is a bearer
	// credential: it prints as "[redacted]" and is never serialized. The SDK
	// never fetches it.
	DownloadFile DownloadURL `json:"-"`

	// ReasonCounts counts the rows per Reason, with every key the provider
	// sent kept as it is.
	ReasonCounts map[Reason]int64 `json:"reason_counts,omitzero"`

	// TotalCounts counts the rows per State and holds other totals such as
	// "processed", "total" and "duplicate", with every key the provider sent
	// kept as it is.
	TotalCounts map[string]int64 `json:"total_counts,omitzero"`
}

// Progress returns the batch's processed and total counts: the top-level
// pair when both are present, else the pair in TotalCounts, and ok false when
// neither pair is complete. Progress is not completion: a batch can report
// every row processed before its results are ready.
func (b *Batch) Progress() (processed, total int64, ok bool) {
	if b == nil {
		return 0, 0, false
	}
	if b.Processed != nil && b.Total != nil {
		return *b.Processed, *b.Total, true
	}
	p, okP := b.TotalCounts["processed"]
	t, okT := b.TotalCounts["total"]
	if okP && okT {
		return p, t, true
	}
	return 0, 0, false
}

// DownloadURL is a signed URL that grants access to a batch's result file
// without the API key. The provider documents it as valid for about five
// days, and documents neither its host, its authentication nor its format
// (the prose says a ZIP file, the example URL ends in .csv), so the SDK
// never fetches it: fetch it with a client you trust, if at all.
//
// It is held behind a function value, so fmt and reflection-based dumpers
// cannot reach it: String, GoString and Format print "[redacted]" (or ""
// for the zero value), and only Reveal returns it. A spreadsheet application
// may execute cells of the file that start with a formula character, such as
// "=", "+", "-" or "@": treat its contents as untrusted.
//
// Compare two values with Equal; == does not compile, and reflect.DeepEqual
// reports two non-zero values as different.
type DownloadURL struct {
	raw func() string
}

// NewDownloadURL returns raw as a DownloadURL, for fakes and for URLs you
// persisted. An empty raw is the zero value.
func NewDownloadURL(raw string) DownloadURL {
	if raw == "" {
		return DownloadURL{}
	}
	return DownloadURL{raw: func() string { return raw }}
}

// Reveal returns the URL. It is a bearer credential: never log it.
func (u DownloadURL) Reveal() string {
	if u.raw == nil {
		return ""
	}
	return u.raw()
}

// IsZero reports whether u holds no URL.
func (u DownloadURL) IsZero() bool { return u.raw == nil }

// Equal reports whether u and v hold the same URL.
func (u DownloadURL) Equal(v DownloadURL) bool { return u.Reveal() == v.Reveal() }

// String returns "[redacted]", or "" for the zero value.
func (u DownloadURL) String() string {
	if u.IsZero() {
		return ""
	}
	return redacted
}

// GoString returns the same text as String.
func (u DownloadURL) GoString() string { return u.String() }

// Format prints the same text as String for every fmt verb (quoted for %q).
// fmt cannot call it for %p on a value, which prints a function address,
// never the URL.
func (u DownloadURL) Format(f fmt.State, verb rune) { formatAs(f, verb, u.String()) }

// GetBatch reads one snapshot of a batch: GET
// https://api.emailable.com/v1/batch.
//
// Free and read-only. It makes exactly one attempt and never retries or
// polls. The snapshot can hold any of the documented shapes: progress counts
// while the batch is processing (without an ID), inline rows (batches of up
// to 1,000 addresses), a DownloadFile (larger batches), counts, or several
// of them; and, once the provider's retention ends (30 days for inline rows,
// 5 days for the file), counts alone. It needs a private key. Errors are a
// *ValidationError (nothing sent) or a *CallError whose BatchID is
// req.BatchID.
func (c *Client) GetBatch(ctx context.Context, req GetBatchRequest) (*Batch, error) {
	if err := c.prepare(ctx); err != nil {
		return nil, err
	}
	r, err := req.request()
	if err != nil {
		return nil, err
	}
	body, cerr := c.call(ctx, r)
	if cerr != nil {
		cerr.BatchID = req.BatchID
		return nil, cerr
	}
	b, derr := decodeBatch(body, req.BatchID)
	if derr != nil {
		cerr = r.decodeFailure(derr)
		cerr.BatchID = req.BatchID
		return nil, cerr
	}
	b.Partial = req.Partial
	return b, nil
}

// request validates r and encodes it as the GET /v1/batch request.
func (r GetBatchRequest) request() (*request, error) {
	if err := checkBatchID("BatchID", r.BatchID); err != nil {
		return nil, err
	}
	q := url.Values{"id": {r.BatchID}}
	if r.Partial {
		q.Set("partial", "true")
	}
	if err := checkSimulation(r.Simulate, Simulation.forGet); err != nil {
		return nil, err
	}
	if r.Simulate != "" {
		q.Set("simulate", string(r.Simulate))
	}
	return &request{op: OpGetBatch, method: http.MethodGet, path: "batch", query: q.Encode()}, nil
}

// Batch members, by index into batchNames.
const (
	batchID = iota
	batchMessage
	batchProcessed
	batchTotal
	batchEmails
	batchDownloadFile
	batchReasonCounts
	batchTotalCounts
	batchCount
)

// batchNames returns the provider names of the batch members, by index.
func batchNames() [batchCount]string {
	return [batchCount]string{
		batchID:           "id",
		batchMessage:      "message",
		batchProcessed:    "processed",
		batchTotal:        "total",
		batchEmails:       "emails",
		batchDownloadFile: "download_file",
		batchReasonCounts: "reason_counts",
		batchTotalCounts:  "total_counts",
	}
}

// decodeBatch decodes a batch snapshot. With wantID set (GetBatch), the
// snapshot is for that batch: an id member, when present, must equal it, and
// the Batch's ID is wantID. Without it (a callback), the id member is
// required, checked first, and becomes the Batch's ID. A snapshot without
// any documented member fails as unexpected_response.
func decodeBatch(body []byte, wantID string) (*Batch, *decodeError) {
	names := batchNames()
	o := readObject(body, names[:])
	switch {
	case wantID == "" && isNull(o.fields[batchID]):
		// A callback names its batch: without an id it is malformed,
		// whatever else it holds.
		return nil, missingField("id")
	case !o.present(batchCount):
		return nil, noMembers()
	}
	b := &Batch{ID: wantID}
	i, derr := b.decode(&o, wantID)
	if derr != nil {
		if derr.field == "" {
			derr.field = names[i]
		}
		return nil, derr
	}
	return b, nil
}

// decode fills b from the members of o, in member order, and on failure
// returns the index of the member concerned.
func (b *Batch) decode(o *object, wantID string) (int, *decodeError) {
	id, derr := decodeBatchID(o.fields[batchID])
	switch {
	case derr != nil:
		return batchID, derr
	case wantID == "":
		// decodeBatch has checked that a callback's id is present.
		b.ID = id
	case id != "" && id != wantID:
		return batchID, idMismatch("")
	}
	if b.Message, derr = decodeText(o.fields[batchMessage], maxTextBytes); derr != nil {
		return batchMessage, derr
	}
	if b.Processed, derr = decodeCount(o.fields[batchProcessed]); derr != nil {
		return batchProcessed, derr
	}
	if b.Total, derr = decodeCount(o.fields[batchTotal]); derr != nil {
		return batchTotal, derr
	}
	if b.Emails, derr = decodeRows(o.fields[batchEmails]); derr != nil {
		return batchEmails, derr
	}
	file, derr := decodeText(o.fields[batchDownloadFile], maxDownloadFileBytes)
	if derr != nil {
		return batchDownloadFile, derr
	}
	b.DownloadFile = NewDownloadURL(file)
	if b.ReasonCounts, derr = decodeCounts(o.fields[batchReasonCounts], "reason_counts", Reason.Known); derr != nil {
		return batchReasonCounts, derr
	}
	if b.TotalCounts, derr = decodeCounts(o.fields[batchTotalCounts], "total_counts", documentedTotal); derr != nil {
		return batchTotalCounts, derr
	}
	return 0, nil
}

// documentedTotal reports whether k is a documented total_counts key: a
// State, "processed" or "total".
func documentedTotal(k string) bool {
	return State(k).Known() || k == "processed" || k == "total"
}

// decodeRows decodes the optional emails member: an array of row objects,
// each with every verification member optional. Null or absent is nil; []
// is an empty, non-nil slice.
func decodeRows(raw []byte) ([]Verification, *decodeError) {
	const field = "emails"
	switch {
	case isNull(raw):
		return nil, nil
	case raw[0] != '[':
		return nil, wrongType(field)
	}
	rows := []Verification{}
	for i, el := range elements(raw) {
		p := path{base: field, index: i}
		if len(el) == 0 || el[0] != '{' {
			return nil, wrongType(p.String())
		}
		v, derr := decodeVerification(el, p, false)
		if derr != nil {
			return nil, derr
		}
		rows = append(rows, v)
	}
	return rows, nil
}
