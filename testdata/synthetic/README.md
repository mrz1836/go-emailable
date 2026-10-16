# Synthetic SDK fixtures

Every file in this directory is a hand-written SDK fixture: a JSON body built
by the SDK's authors from the response shapes the Emailable API v1
documentation describes. None of them is an observed provider response, none
was copied from the provider's documentation, and none was captured from a
live call.

Rules for every fixture here:

- Addresses and URLs use only `example.com` and its subdomains, reserved by
  RFC 2606. No real person, account or mailbox is named.
- No real API key, captcha response, callback token or signed download URL
  appears. A value that stands in for one, such as a download URL's
  signature, is an obvious marker (`sigmarker...`) that tests can search for.
- Where a body has room for it, a member such as `"sdk_fixture"` or a
  `"message"` beginning with `SDK fixture:` labels it as synthetic. The SDK
  ignores members it does not document, so the label never changes a result.
- Files are named `<shape>_<case>.json`: `verify_*` and `account*` for the
  single-verification and account answers, `batch_*` for batch snapshots, and
  `callback_*` for batch completion callbacks.

Tests read a fixture with `testutil.Fixture(t, "synthetic/<name>.json")` and
assert the decoded value field by field, so a fixture documents the shape the
SDK accepts, not the provider's exact output. When the provider's documented
shape changes, change the fixture and the test together.
