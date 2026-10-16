package emailable

// State is the provider's verdict on one address. The set is open: a value
// the provider adds later is kept as sent when it has the documented token
// shape (lowercase letters, digits and "_", up to 64 bytes), and Known reports
// false for it; any other value fails the call as unexpected_response.
// There is deliberately no "safe to send" helper: what to do with each state
// is the caller's policy.
type State string

// Documented states.
const (
	// StateDeliverable means the address accepted the provider's check.
	StateDeliverable State = "deliverable"

	// StateUndeliverable means the address does not exist or cannot receive
	// mail.
	StateUndeliverable State = "undeliverable"

	// StateRisky means the address may receive mail but carries a risk, such
	// as a low-quality, accept-all or role address.
	StateRisky State = "risky"

	// StateUnknown means the provider could not reach a verdict, such as when
	// the mail server did not answer. It is a successful answer, never an
	// error. The provider's pages say both that unknown results are not
	// charged and that they are refunded.
	StateUnknown State = "unknown"

	// StateDuplicate marks a batch row whose address appears more than once
	// in the batch. Which occurrence carries it is undocumented.
	StateDuplicate State = "duplicate"
)

// Known reports whether s is one of the five documented states.
func (s State) Known() bool {
	switch s {
	case StateDeliverable, StateUndeliverable, StateRisky, StateUnknown, StateDuplicate:
		return true
	}
	return false
}

// Reason is the provider's reason for a State. The set is open, as for State.
// The SDK exports no mapping from Reason to State: read both from the
// result.
type Reason string

// Documented reasons.
const (
	ReasonAcceptedEmail     Reason = "accepted_email"
	ReasonLowQuality        Reason = "low_quality"
	ReasonLowDeliverability Reason = "low_deliverability"
	ReasonInvalidEmail      Reason = "invalid_email"
	ReasonInvalidDomain     Reason = "invalid_domain"
	ReasonRejectedEmail     Reason = "rejected_email"
	ReasonInvalidSMTP       Reason = "invalid_smtp"
	ReasonNoConnect         Reason = "no_connect"
	ReasonTimeout           Reason = "timeout"
	ReasonUnavailableSMTP   Reason = "unavailable_smtp"
	ReasonUnexpectedError   Reason = "unexpected_error"
)

// Known reports whether r is one of the eleven documented reasons.
func (r Reason) Known() bool {
	switch r {
	case ReasonAcceptedEmail, ReasonLowQuality, ReasonLowDeliverability, ReasonInvalidEmail,
		ReasonInvalidDomain, ReasonRejectedEmail, ReasonInvalidSMTP, ReasonNoConnect, ReasonTimeout,
		ReasonUnavailableSMTP, ReasonUnexpectedError:
		return true
	}
	return false
}

// Verification is the provider's result for one address: the answer to
// VerifyEmail, and one row of a batch. It holds contact data (the address,
// its parts and inferred names): treat it as personal data.
//
// Every optional field is a pointer or "": nil means the provider did not
// send it, or sent null, so an undetermined check is never mistaken for
// false, 0 or "". In a VerifyEmail result Email and State are always set; in a
// batch row every field is optional, because response_fields can leave any of
// them out.
//
// It survives a JSON round trip through encoding/json unchanged, under the
// provider's snake_case names: nil fields are omitted, a pointer to false, 0
// or "" is kept, and an empty Email, State or Reason is omitted and reads
// back as "". Unmarshaling a raw provider body into it skips every check the
// SDK makes.
type Verification struct {
	// Email is the address as the provider echoes it, never compared to the
	// request or normalized by the SDK.
	Email string `json:"email,omitempty"`

	// State is the verdict; "" means a batch row without it.
	State State `json:"state,omitempty"`

	// Reason is the reason for State, or "" when not sent.
	Reason Reason `json:"reason,omitempty"`

	// Score is the provider's 0 to 100 deliverability score: an observation,
	// on which the SDK sets no policy.
	Score *int `json:"score,omitempty"`

	// AcceptAll reports a domain that accepts every address, so this one
	// could not be verified on its own. nil means not sent. The accept-all
	// check runs only when asked for (VerifyEmailRequest.AcceptAll), so
	// without that, false may mean "not checked" rather than "checked and
	// not accept-all".
	AcceptAll *bool `json:"accept_all,omitempty"`

	// Disposable reports a temporary, disposable address.
	Disposable *bool `json:"disposable,omitempty"`

	// Free reports an address at a free email provider.
	Free *bool `json:"free,omitempty"`

	// MailboxFull reports a mailbox that is full.
	MailboxFull *bool `json:"mailbox_full,omitempty"`

	// NoReply reports a no-reply address.
	NoReply *bool `json:"no_reply,omitempty"`

	// Role reports a role address, such as support@.
	Role *bool `json:"role,omitempty"`

	// BirthYear is the provider's estimate of the owner's birth year, from 0
	// to 9999.
	BirthYear *int `json:"birth_year,omitempty"`

	// DidYouMean is the provider's suggestion for a likely typo. The SDK never
	// applies it.
	DidYouMean *string `json:"did_you_mean,omitempty"`

	// Domain is the domain part of the address.
	Domain *string `json:"domain,omitempty"`

	// User is the local part of the address.
	User *string `json:"user,omitempty"`

	// Tag is the tag of an address such as user+tag@example.com. "" (an
	// address such as user+@example.com) differs from nil (no tag).
	Tag *string `json:"tag,omitempty"`

	// MXRecord is the domain's mail exchanger.
	MXRecord *string `json:"mx_record,omitempty"`

	// SMTPProvider is the provider's name for the mail host.
	SMTPProvider *string `json:"smtp_provider,omitempty"`

	// FirstName, LastName, FullName and Gender are the provider's inferences
	// about the owner.
	FirstName *string `json:"first_name,omitempty"`
	LastName  *string `json:"last_name,omitempty"`
	FullName  *string `json:"full_name,omitempty"`
	Gender    *string `json:"gender,omitempty"`

	// Duration is how long the verification took, in seconds as sent. Only
	// VerifyEmail reports it.
	Duration *float64 `json:"duration,omitempty"`
}

// Verification members, by index into verificationNames.
const (
	verEmail = iota
	verState
	verReason
	verScore
	verAcceptAll
	verDisposable
	verFree
	verMailboxFull
	verNoReply
	verRole
	verBirthYear
	verDidYouMean
	verDomain
	verUser
	verTag
	verMXRecord
	verSMTPProvider
	verFirstName
	verLastName
	verFullName
	verGender
	verDuration
	verCount
)

// Score and birth-year bounds.
const (
	maxScore     = 100
	maxBirthYear = 9999
)

// verificationNames returns the provider names of the verification members,
// by index.
func verificationNames() [verCount]string {
	return [verCount]string{
		verEmail:        "email",
		verState:        "state",
		verReason:       "reason",
		verScore:        "score",
		verAcceptAll:    "accept_all",
		verDisposable:   "disposable",
		verFree:         "free",
		verMailboxFull:  "mailbox_full",
		verNoReply:      "no_reply",
		verRole:         "role",
		verBirthYear:    "birth_year",
		verDidYouMean:   "did_you_mean",
		verDomain:       "domain",
		verUser:         "user",
		verTag:          "tag",
		verMXRecord:     "mx_record",
		verSMTPProvider: "smtp_provider",
		verFirstName:    "first_name",
		verLastName:     "last_name",
		verFullName:     "full_name",
		verGender:       "gender",
		verDuration:     "duration",
	}
}

// decodeVerification decodes the verification object raw at path p. With
// required, email and state must be present (a VerifyEmail answer);
// otherwise every member is optional (a batch row).
func decodeVerification(raw []byte, p path, required bool) (Verification, *decodeError) {
	names := verificationNames()
	o := readObject(raw, names[:])
	var v Verification
	i, derr := v.decode(&o, required)
	if derr != nil {
		return Verification{}, derr.at(p.member(names[i]))
	}
	return v, nil
}

// decode fills v from the members of o, in member order, and on failure
// returns the index of the member concerned.
func (v *Verification) decode(o *object, required bool) (int, *decodeError) {
	var derr *decodeError
	if required {
		v.Email, derr = decodeRequiredText(o.fields[verEmail])
	} else {
		v.Email, derr = decodeText(o.fields[verEmail], maxTextBytes)
	}
	if derr != nil {
		return verEmail, derr
	}
	state, derr := decodeToken(o.fields[verState], required)
	if derr != nil {
		return verState, derr
	}
	v.State = State(state)
	reason, derr := decodeToken(o.fields[verReason], false)
	if derr != nil {
		return verReason, derr
	}
	v.Reason = Reason(reason)
	if v.Score, derr = decodeIntRange(o.fields[verScore], 0, maxScore); derr != nil {
		return verScore, derr
	}
	bools := [...]struct {
		i   int
		dst **bool
	}{
		{verAcceptAll, &v.AcceptAll},
		{verDisposable, &v.Disposable},
		{verFree, &v.Free},
		{verMailboxFull, &v.MailboxFull},
		{verNoReply, &v.NoReply},
		{verRole, &v.Role},
	}
	for _, b := range bools {
		if *b.dst, derr = decodeBool(o.fields[b.i]); derr != nil {
			return b.i, derr
		}
	}
	if v.BirthYear, derr = decodeIntRange(o.fields[verBirthYear], 0, maxBirthYear); derr != nil {
		return verBirthYear, derr
	}
	texts := [...]struct {
		i   int
		dst **string
	}{
		{verDidYouMean, &v.DidYouMean},
		{verDomain, &v.Domain},
		{verUser, &v.User},
		{verTag, &v.Tag},
		{verMXRecord, &v.MXRecord},
		{verSMTPProvider, &v.SMTPProvider},
		{verFirstName, &v.FirstName},
		{verLastName, &v.LastName},
		{verFullName, &v.FullName},
		{verGender, &v.Gender},
	}
	for _, t := range texts {
		if *t.dst, derr = decodeTextPtr(o.fields[t.i]); derr != nil {
			return t.i, derr
		}
	}
	if v.Duration, derr = decodeSeconds(o.fields[verDuration]); derr != nil {
		return verDuration, derr
	}
	return 0, nil
}
