package testutil_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

func TestScript(t *testing.T) {
	respondCalls := 0
	s := testutil.NewScript(
		testutil.Step{Body: "default"},
		testutil.Step{Status: http.StatusTeapot, Header: http.Header{"X-Step": {"2"}}, Body: "teapot"},
		testutil.Step{Err: errBoom},
		testutil.Step{Err: errBoom, Respond: func(r *http.Request) (*http.Response, error) {
			respondCalls++
			return testutil.Response(r, http.StatusAccepted, http.NoBody, 0), nil
		}},
	)

	resp, err := s.RoundTrip(newRequest(t, "first"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a zero Status answers 200")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"), "a nil Header keeps the default")
	assert.Equal(t, int64(len("default")), resp.ContentLength, "the body is declared with its length")
	assert.Equal(t, "default", readAll(t, resp))

	step2 := newRequest(t, "second")
	resp, err = s.RoundTrip(step2)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTeapot, resp.StatusCode)
	assert.Equal(t, http.Header{"X-Step": {"2"}}, resp.Header, "a Header replaces the default")
	assert.Same(t, step2, resp.Request)
	assert.Equal(t, "teapot", readAll(t, resp))

	resp, err = s.RoundTrip(newRequest(t, "third")) //nolint:bodyclose // a failed round trip has no body
	assert.Nil(t, resp)
	require.ErrorIs(t, err, errBoom)

	resp, err = s.RoundTrip(newRequest(t, "fourth"))
	require.NoError(t, err, "Respond wins over Err")
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, 1, respondCalls)

	resp, err = s.RoundTrip(newRequest(t, "fifth")) //nolint:bodyclose // a failed round trip has no body
	assert.Nil(t, resp)
	require.ErrorIs(t, err, testutil.ErrScriptExhausted, "a request past the last step fails")

	assert.Equal(t, 5, s.Calls(), "every request is counted, the one past the end included")
	assert.Equal(t, []string{"/p", "/p", "/p", "/p", "/p"}, s.Paths())
	reqs := s.Requests()
	require.Len(t, reqs, 5)
	for i, want := range []string{"first", "second", "third", "fourth", "fifth"} {
		assert.Equal(t, want, string(reqs[i].Body), "request %d is recorded in order", i)
	}
	reqs[0] = testutil.Request{}
	again := s.Requests()
	require.Len(t, again, 5)
	assert.Equal(t, "first", string(again[0].Body), "Requests returns a copy")
}

func TestReply(t *testing.T) {
	req := newRequest(t, "payload")
	resp, err := testutil.Reply(req, testutil.Step{Status: http.StatusCreated, Body: "made"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "made", readAll(t, resp))
	n, err := req.Body.Read(make([]byte, 1))
	assert.Zero(t, n, "the request body was drained")
	require.ErrorIs(t, err, io.EOF)

	resp, err = testutil.Reply(newRequest(t, ""), testutil.Step{Err: errBoom}) //nolint:bodyclose // a failed round trip has no body
	assert.Nil(t, resp)
	require.ErrorIs(t, err, errBoom)
}
