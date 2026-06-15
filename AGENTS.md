# Agent Instructions

Guidance for agents working in this Go email package.

## Project Overview

This module is `git.bit-monkey.io/bitmonkey/email`. It provides a small `email` package with:

- `Sender` interface and `Message` type in `email.go`
- Resend HTTP sender in `resend.go`
- Mailgun HTTP sender in `mailgun.go`
- SMTP sender in `smtp.go`
- In-memory test double in `mock.go`

Keep the package small and dependency-light. Do not add new providers, configuration layers, or abstractions unless explicitly requested.

## Common Commands

```bash
# Format code
go fmt ./...

# Run tests
go test ./...

# Vet code
go vet ./...

# Full local check
go fmt ./... && go vet ./... && go test ./...
```

The Resend integration test skips unless these environment variables are set:

```bash
RESEND_API_KEY=...
EMAIL_FROM=...
RESEND_TO_ADDRESS=...
```

Never commit or print real API keys, SMTP passwords, or recipient addresses from a private test run.

## Coding Guidelines

- Match the existing simple standard-library style.
- Keep exported API changes intentional and documented with Go comments.
- Keep Mailgun endpoint selection as a simple EU/US region switch; do not expose raw URL configuration unless asked.
- Preserve context cancellation behavior for all `Sender` implementations.
- Classify provider failures with `ErrTransient` or `ErrPermanent` so callers can retry correctly.
- Wrap errors with useful context and `%w` when preserving sentinel errors.
- Use `go fmt`; do not hand-format Go code.

## Testing Guidelines

- Add or update targeted tests for behavior changes.
- Prefer table-driven unit tests for classification and validation cases.
- Use stub transports or local test servers for HTTP behavior; avoid live external calls in normal tests.
- Keep integration tests opt-in via environment variables.

## Before Finishing

Run the strongest practical verification for the change. For code changes, prefer:

```bash
go fmt ./... && go vet ./... && go test ./...
```

If a command cannot be run, state why and what remains unverified.
