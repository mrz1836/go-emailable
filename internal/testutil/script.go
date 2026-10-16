package testutil

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// ErrScriptExhausted is the transport error a Script returns for a request
// past its last step.
var ErrScriptExhausted = errors.New("testutil: script has no step left")

// Step is one scripted answer. Respond, when set, answers the request
// itself; otherwise Err, when set, fails the round trip; otherwise the answer
// is Status (200 when zero) with Header (Content-Type application/json when
// nil) and Body, declared with its length.
type Step struct {
	Status  int
	Header  http.Header
	Body    string
	Err     error
	Respond func(*http.Request) (*http.Response, error)
}

// Script is an in-memory transport that answers requests with its steps, in
// order, and records every request, including those past the last step,
// which fail with ErrScriptExhausted. It is safe for concurrent use.
type Script struct {
	mu    sync.Mutex
	steps []Step
	reqs  []Request
}

// NewScript returns a Script that answers with steps.
func NewScript(steps ...Step) *Script { return &Script{steps: steps} }

// RoundTrip records r and answers with the next step.
func (s *Script) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := Record(r)
	s.mu.Lock()
	i := len(s.reqs)
	s.reqs = append(s.reqs, rec)
	var step Step
	ok := i < len(s.steps)
	if ok {
		step = s.steps[i]
	}
	s.mu.Unlock()
	if !ok {
		return nil, ErrScriptExhausted
	}
	return answer(r, step)
}

// Reply answers r in memory as a Script answers with step, after draining
// the request body, and records nothing: for a Respond func that does
// something first and then answers.
func Reply(r *http.Request, step Step) (*http.Response, error) {
	Drain(r)
	return answer(r, step)
}

// answer answers r with step.
func answer(r *http.Request, step Step) (*http.Response, error) {
	switch {
	case step.Respond != nil:
		return step.Respond(r)
	case step.Err != nil:
		return nil, step.Err
	}
	status := step.Status
	if status == 0 {
		status = http.StatusOK
	}
	resp := Response(r, status, io.NopCloser(strings.NewReader(step.Body)), int64(len(step.Body)))
	if step.Header != nil {
		resp.Header = step.Header.Clone()
	}
	return resp, nil
}

// Requests returns a copy of the recorded requests, in order.
func (s *Script) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// Calls returns the number of round trips so far.
func (s *Script) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// Paths returns the URL path of each recorded request, in order.
func (s *Script) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.reqs))
	for i, r := range s.reqs {
		out[i] = r.Path
	}
	return out
}
