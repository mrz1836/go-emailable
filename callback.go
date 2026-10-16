package emailable

import (
	"errors"
	"io"
)

// DecodeBatchCallback decodes the body of the provider's batch callback, the
// POST it sends to CreateBatchRequest.CallbackURL when a batch completes. The
// body has the shape of a GetBatch answer, with a required id.
//
// It reads r to EOF, but at most maxBytes+1 bytes, so that a longer body is
// detected and fails as response_too_large. maxBytes must be from 1 KiB to
// 64 MiB (DefaultMaxResponseBytes suits most batches). It applies the same
// strict checks as GetBatch, makes no request and never closes r.
//
// Decoding is not authentication: the provider documents no signature, no
// event ID, no ordering and no exactly-once delivery, and retries a callback
// hourly for up to three days until it gets HTTP 200. Check a secret token of
// your own in the callback URL before calling it, tolerate duplicate
// deliveries, and treat a callback as a hint: re-read the batch with GetBatch
// before acting on it.
//
// Errors are a *ValidationError for a nil r or a maxBytes out of range, or a
// *CallError with Op batch_callback, no StatusCode and no Attempts: transport
// when r fails, response_too_large past maxBytes, and the GetBatch kinds for
// a body that does not decode. Its BatchID is the body's id when one could be
// read.
func DecodeBatchCallback(r io.Reader, maxBytes int64) (*Batch, error) {
	switch {
	case r == nil:
		return nil, invalidRequest("r", reasonNil)
	case maxBytes < minResponseBytesLimit || maxBytes > maxResponseBytesLimit:
		return nil, invalidRequest("maxBytes", reasonOutOfRange)
	}
	body, err := readBody(r, -1, maxBytes)
	switch {
	case errors.Is(err, errTransportStalled):
		return nil, newCallError(OpBatchCallback, FailureTransport, 0, errTransportStalled)
	case err != nil:
		return nil, newCallError(OpBatchCallback, FailureTransport, 0, errCallbackRead)
	case int64(len(body)) > maxBytes:
		return nil, newCallError(OpBatchCallback, FailureResponseTooLarge, 0, errResponseTooLarge)
	}
	if kind, cause := checkStrict(body); cause != nil {
		return nil, callbackFailure(body, &decodeError{kind: kind, cause: cause})
	}
	b, derr := decodeBatch(body, "")
	if derr != nil {
		return nil, callbackFailure(body, derr)
	}
	return b, nil
}

// callbackFailure returns the CallError for a callback body that failed to
// decode with derr, with the body's batch ID when it can still be read
// without doubt.
func callbackFailure(body []byte, derr *decodeError) *CallError {
	ce := newCallError(OpBatchCallback, derr.kind, 0, derr.cause)
	ce.Field = derr.field
	ce.BatchID, _ = rescueBatchID(body)
	return ce
}
