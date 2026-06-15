# email

Small Go email sending package with SMTP, Resend, and in-memory mock senders.

```go
package main

import (
    "context"

    "git.bit-monkey.io/bitmonkey/email"
)

func main() {
    sender := email.NewResendSender("re_...", "Steeev <noreply@example.com>")

    _ = sender.Send(context.Background(), email.Message{
        To:       "user@example.com",
        Subject:  "Welcome",
        HTMLBody: "<p>Hello!</p>",
        TextBody: "Hello!",
    })
}
```

## Senders

- `email.NewResendSender(apiKey, defaultFrom)`
- `email.NewSMTPSender(host, port, user, password, defaultFrom)`
- `email.NewMockSender()`

All senders implement:

```go
type Sender interface {
    Send(ctx context.Context, msg Message) error
}
```

Provider errors wrap either:

- `email.ErrTransient` for retryable failures such as network errors, 5xx responses, or rate limits.
- `email.ErrPermanent` for non-retryable failures such as invalid requests or 5xx-class SMTP permanent failures.

## Private module setup

For private Gitea usage, configure Go once on each machine:

```sh
go env -w GOPRIVATE=git.bit-monkey.io/bitmonkey/*
```
