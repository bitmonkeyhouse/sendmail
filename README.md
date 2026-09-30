# sendmail

Small Go email sending package with SMTP, Resend, Mailgun, and in-memory mock senders.

```go
package main

import (
    "context"

    "github.com/bitmonkeyhouse/sendmail"
)

func main() {
    sender := sendmail.NewResendSender("re_...", "Steeev <noreply@example.com>")

    _ = sender.Send(context.Background(), sendmail.Message{
        To:       "user@example.com",
        Subject:  "Welcome",
        HTMLBody: "<p>Hello!</p>",
        TextBody: "Hello!",
    })
}
```

## Senders

Use the high-level factory methods when you want this package to choose the provider, or the explicit constructors when your app wants to choose the provider itself.

| Method | Function |
| --- | --- |
| `sendmail.NewSender(config)` | Creates a `Sender` from `sendmail.Config`, validates provider-specific settings, and returns the selected transport. Use this when your app already has parsed config. |
| `sendmail.NewSenderFromEnv()` | Reads environment variables, validates them, and returns the selected transport. Use this when you want provider switching to live entirely in this package. |
| `sendmail.NewResendSender(apiKey, defaultFrom)` | Creates a Resend HTTP sender directly. |
| `sendmail.NewResendSenderWithClient(apiKey, defaultFrom, client)` | Creates a Resend sender with a custom `http.Client`, mainly for tests. |
| `sendmail.NewMailgunSender(apiKey, domain, defaultFrom)` | Creates a Mailgun HTTP sender directly, using the EU endpoint by default. |
| `sendmail.NewMailgunSenderWithConfig(apiKey, domain, defaultFrom, config)` | Creates a Mailgun sender with Mailgun-specific config, such as `MailgunRegionUS`. |
| `sendmail.NewMailgunSenderWithClient(apiKey, domain, defaultFrom, client)` | Creates a Mailgun sender with a custom `http.Client`, mainly for tests. |
| `sendmail.NewSMTPSender(host, port, user, password, defaultFrom)` | Creates an SMTP sender using PLAIN auth. |
| `sendmail.NewMockSender()` | Creates an in-memory test double that records sent messages. |

Mailgun defaults to the EU API endpoint. Use `sendmail.MailgunRegionUS` in `sendmail.MailgunConfig` for US domains.

### Config-based provider selection

Use `NewSender` when you want provider switching in this package but prefer to parse configuration yourself:

```go
sender, err := sendmail.NewSender(sendmail.Config{
    Provider:     sendmail.ProviderResend,
    DefaultFrom:  "Your App <noreply@example.com>",
    ResendAPIKey: "re_...",
})
if err != nil {
    return err
}

err = sender.Send(ctx, msg)
```

Provider constants are:

- `sendmail.ProviderResend`
- `sendmail.ProviderMailgun`
- `sendmail.ProviderSMTP`

### Environment-based provider selection

Use `NewSenderFromEnv` when you want the package to read config and choose the provider:

```go
sender, err := sendmail.NewSenderFromEnv()
if err != nil {
    return err
}

err = sender.Send(ctx, msg)
```

Explicit constructors do not read environment variables; callers pass those values directly as arguments.

### Environment variables

| Variable | Used by | Required | Function |
| --- | --- | --- | --- |
| `EMAIL_PROVIDER` | `NewSenderFromEnv` | Yes | Selects the provider: `resend`, `mailgun`, or `smtp`. |
| `EMAIL_FROM` | `NewSenderFromEnv`, all providers | Yes | Default sender address used when `Message.From` is empty. Equivalent to the `defaultFrom`/`from` constructor argument. |
| `RESEND_API_KEY` | Resend | Yes for `EMAIL_PROVIDER=resend` | Resend API key used as the bearer token for API requests. |
| `MAILGUN_API_KEY` | Mailgun | Yes for `EMAIL_PROVIDER=mailgun` | Mailgun API key used for HTTP Basic auth. |
| `MAILGUN_DOMAIN` | Mailgun | Yes for `EMAIL_PROVIDER=mailgun` | Mailgun sending domain used in the `/v3/{domain}/messages` endpoint. |
| `MAILGUN_REGION` | Mailgun | No | Mailgun API region: `eu` or `us`. Defaults to `eu` when unset. |
| `SMTP_HOST` | SMTP | Yes for `EMAIL_PROVIDER=smtp` | SMTP server hostname. |
| `SMTP_PORT` | SMTP | Yes for `EMAIL_PROVIDER=smtp` | SMTP server port, for example `587`. Must be an integer from 1 to 65535. |
| `SMTP_USER` | SMTP | Yes for `EMAIL_PROVIDER=smtp` | SMTP username for PLAIN auth. |
| `SMTP_PASSWORD` | SMTP | Yes for `EMAIL_PROVIDER=smtp` | SMTP password for PLAIN auth. |
| `RESEND_TO_ADDRESS` | Resend integration test only | Yes for `TestResendSender_Integration` | Recipient address for the opt-in live Resend integration test. It is not used by runtime sender configuration. |

All senders implement:

```go
type Sender interface {
    Send(ctx context.Context, msg Message) error
}
```

Provider errors wrap either:

- `sendmail.ErrTransient` for retryable failures such as network errors, 5xx responses, or rate limits.
- `sendmail.ErrPermanent` for non-retryable failures such as invalid requests or 5xx-class SMTP permanent failures.

## Releases

Every merged PR to `main` creates a GitHub release and version tag. Label the PR `semver:major` for breaking changes or `semver:minor` for new features; otherwise it gets a patch bump. If both labels are present, major wins.
