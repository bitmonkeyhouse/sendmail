// Package sendmail sends email in Go using SMTP, the Resend API, or the Mailgun
// API, with an in-memory mock sender for testing.
//
// Messages support HTML and plain-text email bodies, a reply-to address, and a
// default sender address. SMTP supports STARTTLS and implicit TLS; Resend and
// Mailgun use HTTP APIs. All senders implement the Sender interface.
//
// A caller builds a Message and passes it to a Sender. Production senders use
// their configured default From address when Message.From is empty and classify
// delivery failures with ErrTransient or ErrPermanent so callers can decide
// whether to retry. MockSender records messages unchanged and can return injected
// errors.
//
// Senders may be created directly with the provider constructors (for example,
// NewResendSender or NewSMTPSender), through NewSender with an explicit Config,
// or through NewSenderFromEnv. The direct constructors do not validate their
// arguments; validation happens in Send for SMTP and is left to the provider for
// the HTTP senders, whereas the factory constructors validate configuration
// eagerly.
//
// Production senders respect context cancellation and deadlines. MockSender is a
// simple test double that ignores the context.
package sendmail
