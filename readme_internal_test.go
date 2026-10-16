package emailable

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readmeSources are the compiled files whose code the README's Go blocks must
// quote: the package's examples and the example programs.
func readmeSources() []string {
	return []string{"example_test.go", "examples/main.go", "examples/quickstart/main.go"}
}

// readmeDocs are the Markdown files whose links and placeholders are checked.
func readmeDocs() []string {
	return []string{"README.md", "docs/adding-an-operation.md"}
}

// readmeFile returns the Markdown file name, failing tb when it cannot be read.
func readmeFile(tb testing.TB, name string) string {
	tb.Helper()
	data, err := os.ReadFile(filepath.FromSlash(name))
	require.NoError(tb, err)
	return string(data)
}

// readmeProse returns md without its fenced code blocks, so that code such as
// errors.AsType[*emailable.CallError](err) is never read as a link.
func readmeProse(md string) string {
	return regexp.MustCompile("(?s)```.*?```").ReplaceAllString(md, "")
}

// readmeNormalize collapses every run of whitespace to one space, so that a
// README block matches its source at any indentation.
func readmeNormalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// readmeFences returns the content of every fenced code block in md whose
// info string is lang.
func readmeFences(md, lang string) []string {
	re := regexp.MustCompile("(?s)```" + regexp.QuoteMeta(lang) + "\n(.*?)\n```")
	matches := re.FindAllStringSubmatch(md, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func TestReadmeGoBlocksAreCompiled(t *testing.T) {
	var sources strings.Builder
	for _, name := range readmeSources() {
		sources.WriteString(readmeNormalize(readmeFile(t, name)))
		sources.WriteString("\n")
	}
	compiled := sources.String()
	blocks := readmeFences(readmeFile(t, "README.md"), "go")
	require.NotEmpty(t, blocks)
	for i, block := range blocks {
		assert.Contains(t, compiled, readmeNormalize(block), "README Go block %d is not quoted verbatim from %v:\n%s",
			i+1, readmeSources(), block)
	}
}

func TestReadmeHasNoPlaceholders(t *testing.T) {
	for _, doc := range readmeDocs() {
		assert.NotContains(t, readmeFile(t, doc), "<!--", "every placeholder in %s must be filled in", doc)
	}
}

func TestReadmeRelativeLinksResolve(t *testing.T) {
	re := regexp.MustCompile(`(?:\]\(|href=")([^)"#][^)"]*)`)
	for _, doc := range readmeDocs() {
		links := re.FindAllStringSubmatch(readmeProse(readmeFile(t, doc)), -1)
		require.NotEmpty(t, links, "%s has relative links", doc)
		for _, m := range links {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			_, err := os.Stat(filepath.Join(filepath.Dir(doc), filepath.FromSlash(target)))
			assert.NoError(t, err, "%s links to a missing file: %s", doc, target)
		}
	}
}

// readmeSlug returns GitHub's anchor for a heading: lowercase, keeping
// letters, marks, numbers, underscores, hyphens and spaces, with each space
// turned into a hyphen.
func readmeSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestReadmeSlug(t *testing.T) {
	assert.Equal(t, "-examples--tests", readmeSlug("🧪 Examples & Tests"))
	assert.Equal(t, "http-249-and-waitforverification", readmeSlug("HTTP 249 and WaitForVerification"))
	assert.Equal(t, "the-transport-proxies-and-tracing", readmeSlug("The transport, proxies and tracing"))
	assert.Equal(t, "\ufe0f-behavior", readmeSlug("⚙️ Behavior"), "the emoji's variation selector is a mark")
}

func TestReadmeAnchorsMatchHeadings(t *testing.T) {
	for _, doc := range readmeDocs() {
		md := readmeFile(t, doc)
		slugs := map[string]bool{}
		inFence := false
		for line := range strings.SplitSeq(md, "\n") {
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				continue
			}
			if inFence || !strings.HasPrefix(line, "#") {
				continue
			}
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			heading = strings.ReplaceAll(heading, "&nbsp;", " ")
			slugs[readmeSlug(heading)] = true
		}
		anchors := regexp.MustCompile(`(?:\]\(|href=")#([^)"]+)`).FindAllStringSubmatch(readmeProse(md), -1)
		if doc == "README.md" {
			require.NotEmpty(t, anchors)
		}
		for _, m := range anchors {
			anchor, err := url.PathUnescape(m[1])
			require.NoError(t, err)
			assert.True(t, slugs[anchor], "%s links to #%s, which no heading produces", doc, m[1])
		}
	}
}

// TestReadmeLimitsMatchCode checks every default and limit the README states
// against the constant that enforces it. A claim may wrap across lines.
func TestReadmeLimitsMatchCode(t *testing.T) {
	md := readmeNormalize(readmeFile(t, "README.md"))
	tests := []struct {
		claim string
		ok    bool
	}{
		{"30 seconds (`DefaultTimeout`)", DefaultTimeout == 30*time.Second},
		{"more than 0, at most 10 minutes", maxTimeout == 10*time.Minute},
		{"8 MiB (`DefaultMaxResponseBytes`)", DefaultMaxResponseBytes == 8<<20},
		{"1 KiB to 64 MiB", minResponseBytesLimit == 1<<10 && maxResponseBytesLimit == 64<<20},
		{"also capped at 1 MiB", smallResponseCap == 1<<20},
		{"| Every other response | 1 MiB,", smallResponseCap == 1<<20},
		{"50,000 (`DefaultMaxBatchEmails`)", DefaultMaxBatchEmails == 50_000},
		{"1 to 1,000,000", maxBatchEmailsLimit == 1_000_000},
		{"| API key | 1 to 4096 bytes of visible ASCII |", maxKeyBytes == 4096},
		{"1 to 254 bytes of UTF-8", maxEmailBytes == 254},
		{"whole seconds from 2s to 10s", minProviderTimeout == 2*time.Second && maxProviderTimeout == 10*time.Second},
		{"at least 5 seconds below `WithTimeout`", providerTimeoutHeadroom == 5*time.Second},
		{"keep `WithTimeout` at 10 seconds or more", providerTimeoutHeadroom+5*time.Second == 10*time.Second},
		{"| `CaptchaResponse` | at most 8 KiB |", maxCaptchaBytes == 8<<10},
		{"| Encoded `CreateBatch` body | 64 MiB |", maxBatchRequestBytes == 64<<20},
		{"| `CallbackURL` | `https`, at most 2048 bytes |", maxCallbackURLBytes == 2048},
		{"| Batch ID | 1 to 256 bytes of visible ASCII |", maxBatchIDBytes == 256},
		{
			"`Interval` 1 second to 30 seconds, 2 seconds when 0",
			minPendingInterval == time.Second && maxPendingInterval == 30*time.Second &&
				DefaultPendingInterval == 2*time.Second,
		},
		{"`MaxAttempts` 2 to 30", minPendingAttempts == 2 && maxPendingAttempts == 30},
		{"`MaxWait` more than the effective `Interval`, up to 4 minutes (`MaxPendingWait`)", MaxPendingWait == 4*time.Minute},
		{"HTTP 249 (`StatusPending`)", StatusPending == 249},
		{"| Error response body read | 64 KiB, best-effort", maxErrorBodyBytes == 64<<10},
		{
			"| Response headers (SDK-built transport) | 64 KiB |",
			maxResponseHeaderBytes == 64<<10 && newTransport().MaxResponseHeaderBytes == 64<<10,
		},
		{"100,000 / 1,000,000", maxContainers == 100_000 && maxValues == 1_000_000},
		{"4 KiB / 64 bytes / 8 KiB", maxTextBytes == 4<<10 && maxTokenBytes == 64 && maxDownloadFileBytes == 8<<10},
		{"| Keys in `reason_counts` or `total_counts` | 256 each |", maxCountKeys == 256},
		{"0 to 100 / 0 to 9999", maxScore == 100 && maxBirthYear == 9999},
		{"cut to 1 KiB", maxDiagnosticMessageBytes == 1<<10},
		{"100 empty reads in a row", maxEmptyReads == 100},
		{"1 to 19 decimal digits", maxHeaderDigits == 19},
		{"idle connections close on their own after 90 seconds", newTransport().IdleConnTimeout == 90*time.Second},
		{"at most 2048 bytes of valid UTF-8", maxCallbackURLBytes == 2048},
		{"the encoded body must fit 64 MiB", maxBatchRequestBytes == 64<<20},
		{"encoded body would exceed 64 MiB", maxBatchRequestBytes == 64<<20},
		{"(one over 64 bytes included) and a batch ID over 256 bytes", maxTokenBytes == 64 && maxBatchIDBytes == 256},
		{"less than 5 seconds below `WithTimeout`", providerTimeoutHeadroom == 5*time.Second},
	}
	for _, tt := range tests {
		assert.Contains(t, md, readmeNormalize(tt.claim))
		assert.True(t, tt.ok, "the code no longer matches the README claim %q", tt.claim)
	}
}

// readmeTable returns the rows of the Markdown table in md whose header row is
// header, each split into its trimmed cells.
func readmeTable(tb testing.TB, md, header string) [][]string {
	tb.Helper()
	_, rest, ok := strings.Cut(md, header+"\n")
	require.True(tb, ok, "the README has the table %q", header)
	var rows [][]string
	for line := range strings.SplitSeq(rest, "\n") {
		if !strings.HasPrefix(line, "|") {
			break
		}
		if strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), " | ")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, cells)
	}
	require.NotEmpty(tb, rows, "the table %q has rows", header)
	return rows
}

// TestReadmeStatusTableMatchesCode checks each status row of the error
// reference against statusKind: every status it names has the row's kind,
// for every operation it applies to, and no status row goes unchecked.
func TestReadmeStatusTableMatchesCode(t *testing.T) {
	all := []Operation{OpVerifyEmail, OpCreateBatch, OpGetBatch, OpAccountInfo}
	tests := []struct {
		row   string // the start of the row's first cell
		ops   []Operation
		codes []int
		kind  FailureKind
	}{
		{"HTTP 249 from `VerifyEmail`", []Operation{OpVerifyEmail}, []int{249}, FailurePending},
		{"HTTP 401 or 403", all, []int{401, 403}, FailureUnauthorized},
		{"HTTP 402", all, []int{402}, FailurePaymentRequired},
		{"HTTP 404", all, []int{404}, FailureNotFound},
		{"HTTP 429", all, []int{429}, FailureRateLimited},
		{"Any other HTTP 4xx", all, []int{400, 405, 409, 410, 418, 422, 451, 499}, FailureRejected},
		{"HTTP 503", all, []int{503}, FailureUnavailable},
		{"Any other HTTP 5xx", all, []int{500, 501, 502, 504, 599}, FailureUpstream},
		{
			"HTTP 1xx, 3xx (never followed), a 2xx other than 200, 600 and above,", all,
			[]int{100, 101, 199, 201, 204, 299, 300, 301, 302, 304, 307, 308, 399, 600, 999},
			FailureUnexpectedResponse,
		},
		{
			"HTTP 1xx, 3xx (never followed), a 2xx other than 200, 600 and above, or 249 from any operation but `VerifyEmail`",
			[]Operation{OpCreateBatch, OpGetBatch, OpAccountInfo},
			[]int{249},
			FailureUnexpectedResponse,
		},
	}
	rows := readmeTable(t, readmeFile(t, "README.md"),
		"| What happened | `Kind` (constant) | `errors.Is` matches | `OutcomeUnknown` on a paid call |")
	checked := map[string]bool{}
	for _, tt := range tests {
		i := slices.IndexFunc(rows, func(r []string) bool { return strings.HasPrefix(r[0], tt.row) })
		require.GreaterOrEqual(t, i, 0, "the error reference has a row %q", tt.row)
		checked[rows[i][0]] = true
		assert.Contains(t, rows[i][1], "`"+string(tt.kind)+"`", "the row %q names its kind", tt.row)
		for _, op := range tt.ops {
			for _, code := range tt.codes {
				assert.Equal(t, tt.kind, statusKind(op, code), "%s HTTP %d, as the row %q says", op, code, tt.row)
			}
		}
	}
	for _, r := range rows {
		if strings.HasPrefix(r[0], "HTTP ") || strings.HasPrefix(r[0], "Any other HTTP") {
			assert.True(t, checked[r[0]], "the status row %q is checked against statusKind", r[0])
		}
	}
}

// readmeConsts returns the string constants of the package's non-test source
// files whose type is named typ, or, for an empty typ, the untyped ones whose
// name starts with prefix, by name.
func readmeConsts(tb testing.TB, typ, prefix string) map[string]string {
	tb.Helper()
	names, err := filepath.Glob("*.go")
	require.NoError(tb, err)
	out := map[string]string{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		require.NoError(tb, err)
		for _, spec := range readmeConstSpecs(f) {
			if readmeTypeName(spec.Type) != typ {
				continue
			}
			for i, ident := range spec.Names {
				lit, ok := spec.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || !strings.HasPrefix(ident.Name, prefix) {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				require.NoError(tb, err)
				out[ident.Name] = v
			}
		}
	}
	require.NotEmpty(tb, out, "the package has %s constants", typ+prefix)
	return out
}

// readmeConstSpecs returns the value specs of f's const declarations that
// give each name a value.
func readmeConstSpecs(f *ast.File) []*ast.ValueSpec {
	var out []*ast.ValueSpec
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Values) == len(vs.Names) {
				out = append(out, vs)
			}
		}
	}
	return out
}

// readmeTypeName returns the name of a type expression that is a plain
// identifier, or "".
func readmeTypeName(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// readmeAssertPairs asserts that md spells every constant of consts as
// "`value` (`Name`)", and that every such pair in md whose name starts with
// prefix names a constant with that value.
func readmeAssertPairs(tb testing.TB, md, prefix string, consts map[string]string) {
	tb.Helper()
	for name, value := range consts {
		assert.Contains(tb, md, "`"+value+"` (`"+name+"`)", "the README names %s", name)
	}
	re := regexp.MustCompile("`([a-z_]+)` \\(`(" + prefix + "\\w+)`\\)")
	for _, m := range re.FindAllStringSubmatch(md, -1) {
		v, ok := consts[m[2]]
		assert.True(tb, ok, "the README names %s, which is not a constant", m[2])
		assert.Equal(tb, v, m[1], "the README spells %s", m[2])
	}
}

// TestReadmeConstantsMatchCode checks the README's lists of constants and
// their counts against the constants the package declares.
func TestReadmeConstantsMatchCode(t *testing.T) {
	md := readmeFile(t, "README.md")
	t.Run("FailureKind", func(t *testing.T) {
		readmeAssertPairs(t, md, "Failure", readmeConsts(t, "FailureKind", ""))
	})
	t.Run("Operation", func(t *testing.T) {
		readmeAssertPairs(t, md, "Op", readmeConsts(t, "Operation", ""))
	})
	t.Run("State", func(t *testing.T) {
		states := readmeConsts(t, "State", "")
		table := readmeTable(t, md, "| `State` | Meaning |")
		rows := make([]string, 0, len(table))
		for _, r := range table {
			rows = append(rows, strings.Trim(r[0], "`"))
		}
		assert.ElementsMatch(t, slices.Collect(maps.Keys(states)), rows, "the state table lists every State")
		for _, v := range states {
			assert.True(t, State(v).Known(), v)
		}
	})
	t.Run("Reason", func(t *testing.T) {
		reasons := readmeConsts(t, "Reason", "")
		m := regexp.MustCompile(`the SDK defines the (\w+) documented reasons: ([^.]+)\.`).
			FindStringSubmatch(readmeNormalize(md))
		require.NotNil(t, m)
		assert.Equal(t, "eleven", m[1])
		assert.Len(t, reasons, 11, "the README says eleven")
		found := regexp.MustCompile("`(Reason\\w+)`").FindAllStringSubmatch(m[2], -1)
		listed := make([]string, 0, len(found))
		for _, r := range found {
			listed = append(listed, r[1])
		}
		assert.ElementsMatch(t, slices.Collect(maps.Keys(reasons)), listed)
		for _, v := range reasons {
			assert.True(t, Reason(v).Known(), v)
		}
	})
	t.Run("Simulation", func(t *testing.T) {
		for name, v := range readmeConsts(t, "Simulation", "") {
			assert.Contains(t, md, "`"+name+"`")
			assert.True(t, Simulation(v).Known(), v)
		}
	})
	t.Run("ResponseField", func(t *testing.T) {
		fields := readmeConsts(t, "ResponseField", "")
		assert.Contains(t, md, "(the "+strconv.Itoa(len(fields))+" documented fields: every `Verification` member but `duration`)")
		names := verificationNames()
		want := slices.DeleteFunc(names[:], func(n string) bool { return n == "duration" })
		assert.ElementsMatch(t, want, slices.Collect(maps.Values(fields)))
	})
	t.Run("validation reasons", func(t *testing.T) {
		var listed []string
		for _, r := range readmeTable(t, md, "| `Reason` | Meaning |") {
			for code := range strings.SplitSeq(r[0], " / ") {
				listed = append(listed, strings.Trim(code, "`"))
			}
		}
		assert.ElementsMatch(t, slices.Collect(maps.Values(readmeConsts(t, "", "reason"))), listed)
	})
}
