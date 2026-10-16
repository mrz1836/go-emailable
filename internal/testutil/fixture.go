package testutil

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fixture returns the contents of the file name, a slash-separated path under
// testdata, failing tb when it cannot be read.
func Fixture(tb testing.TB, name string) []byte {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	require.NoError(tb, err)
	return data
}

// FullVerification is an SDK fixture, synthetic and compact: a verification
// object with every documented member, for benchmarks and allocation guards.
const FullVerification = `{"accept_all":false,"did_you_mean":null,"disposable":false,"domain":"example.com",` +
	`"duration":0.493,"email":"jane.doe@example.com","first_name":"Jane","free":false,"full_name":"Jane Doe",` +
	`"gender":"female","last_name":"Doe","mailbox_full":false,"mx_record":"mx.example.com","no_reply":false,` +
	`"reason":"accepted_email","role":false,"score":100,"smtp_provider":"google","state":"deliverable",` +
	`"tag":null,"user":"jane.doe","birth_year":1987}`

// AppendBatchRow appends synthetic batch row i to b and returns the result:
// a verification object for user.<i>@example.com, with one container and 19
// values, whose first_name carries pad bytes.
func AppendBatchRow(b []byte, i, pad int) []byte {
	id := strconv.Itoa(i)
	b = append(b, `{"email":"user.`...)
	b = append(b, id...)
	b = append(b, `@example.com","state":"deliverable","reason":"accepted_email","score":95,"accept_all":false,`...)
	b = append(b, `"disposable":false,"free":false,"mailbox_full":false,"no_reply":false,"role":false,`...)
	b = append(b, `"domain":"example.com","user":"user.`...)
	b = append(b, id...)
	b = append(b, `","tag":null,"mx_record":"mx.example.com","smtp_provider":"example","first_name":"`...)
	b = append(b, strings.Repeat("n", pad)...)
	b = append(b, `","last_name":"Doe","full_name":"User Doe","gender":"female"}`...)
	return b
}
