package emailable

import (
	"context"
	"net/http"
)

// AccountInfoRequest is one GET /v1/account call. It has no fields today;
// it exists so that fields can be added without breaking callers.
type AccountInfoRequest struct{}

// AccountInfo is the provider's answer to GET /v1/account.
type AccountInfo struct {
	// OwnerEmail is the account owner's address: personal data.
	OwnerEmail string `json:"owner_email"`

	// AvailableCredits is the account's credit balance when the provider
	// answered. It is not a reservation or a spending cap: concurrent calls
	// and auto-refill can change it at any time.
	AvailableCredits int64 `json:"available_credits"`
}

// AccountInfo reads the account's owner and credit balance: GET
// https://api.emailable.com/v1/account.
//
// Free and read-only. It makes exactly one attempt and never retries. It
// needs a private key. Errors are a *ValidationError (nothing sent) or a
// *CallError.
func (c *Client) AccountInfo(ctx context.Context, _ AccountInfoRequest) (*AccountInfo, error) {
	if err := c.prepare(ctx); err != nil {
		return nil, err
	}
	r := &request{op: OpAccountInfo, method: http.MethodGet, path: "account"}
	body, cerr := c.call(ctx, r)
	if cerr != nil {
		return nil, cerr
	}
	info, derr := decodeAccountInfo(body)
	if derr != nil {
		return nil, r.decodeFailure(derr)
	}
	return info, nil
}

// decodeAccountInfo decodes a GET /v1/account answer: owner_email, a JSON
// string, and available_credits, a count, both required.
func decodeAccountInfo(body []byte) (*AccountInfo, *decodeError) {
	o := readObject(body, []string{"owner_email", "available_credits"})
	if isNull(o.fields[0]) {
		return nil, missingField("owner_email")
	}
	owner, derr := decodeText(o.fields[0], maxTextBytes)
	if derr != nil {
		return nil, derr.at("owner_email")
	}
	credits, derr := decodeRequiredCount(o.fields[1])
	if derr != nil {
		return nil, derr.at("available_credits")
	}
	return &AccountInfo{OwnerEmail: owner, AvailableCredits: credits}, nil
}
