// Package tokens gives a cheap offline token estimate.
//
// It is intentionally rough (~4 bytes of JSON per token) and only used to
// compare a request before and after optimization inside the pipeline and as
// a fallback. Exact numbers come from count_tokens (see proxy.Handler) and
// from the usage block of the API response.
package tokens

import "github.com/adoonaai/shorttok/internal/anthropic"

const bytesPerToken = 4

func Estimate(r *anthropic.Request) int {
	return (len(r.System) + messageBytes(r.Messages)) / bytesPerToken
}

func EstimateMessages(msgs []anthropic.Message) int {
	return messageBytes(msgs) / bytesPerToken
}

func messageBytes(msgs []anthropic.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content) + 8 // role and framing
	}
	return n
}
