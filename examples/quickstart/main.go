// Command quickstart verifies one address against the live Emailable API,
// with the key in EMAILABLE_API_KEY. It makes one VerifyEmail call: one
// credit with a live key, none with a test key (one that starts with
// "test_"). No test ever runs it; the offline demo is in the parent
// directory (go run ./examples).
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/mrz1836/go-emailable"
)

func main() {
	// The application chooses where the key comes from; the SDK reads no environment.
	apiKey := os.Getenv("EMAILABLE_API_KEY")
	if apiKey == "" {
		log.Fatal("set EMAILABLE_API_KEY first (a test_ key uses no credits)")
	}
	client, err := emailable.NewClient(apiKey)
	if err != nil {
		log.Fatal(err) // ErrInvalidConfig: no call was made
	}

	// One paid call (one credit with a live key), whatever the result.
	v, err := client.VerifyEmail(context.Background(), emailable.VerifyEmailRequest{
		Email: "jane.doe@example.com",
	})
	if err != nil {
		log.Fatal(err) // ErrPending (HTTP 249) is not a result; after ErrOutcomeUnknown it may be billed
	}
	fmt.Println(v.State, v.Reason) // "unknown" is an answer, not an error
}
