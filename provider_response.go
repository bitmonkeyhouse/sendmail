package sendmail

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	// providerResponseLimit caps HTTP response bodies at 64 KiB, regardless of status.
	providerResponseLimit = 64 << 10
	// providerDetailLimit caps provider error text at 1 KiB, excluding the marker.
	providerDetailLimit      = 1 << 10
	providerDetailTruncation = " [truncated]"
)

// readProviderResponse reads at most limit+1 bytes to detect overflow without
// draining the body. This bounds memory, not read time; callers supply deadlines.
func readProviderResponse(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, providerResponseLimit+1))
	if len(body) > providerResponseLimit {
		return nil, fmt.Errorf("%w: response exceeds 64 KiB limit", ErrTransient)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %w", ErrTransient, err)
	}
	return body, nil
}

func limitProviderDetail(detail string) string {
	// Raw-text responses may contain invalid UTF-8; keep error text valid too.
	detail = strings.ToValidUTF8(detail, "\uFFFD")
	if len(detail) <= providerDetailLimit {
		return detail
	}
	end := providerDetailLimit
	for !utf8.RuneStart(detail[end]) {
		end--
	}
	return detail[:end] + providerDetailTruncation
}
