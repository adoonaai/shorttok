// Package history folds old conversation turns into a summary made by a
// cheap model, keeping the last turns verbatim.
//
// Clients keep sending the full history; the proxy rewrites it on the fly:
//
//	[m0 ... m(k-1)] [mk ... mn]   ->   system += summary(m0..m(k-1)),  messages = [mk ... mn]
//
// Summaries are cached by a hash chain over the message prefix and built
// incrementally: summary(0..k) = summarize(summary(0..j), messages j..k).
// The cut point is aligned to Chunk, so a new summary is needed only once
// every few turns, and in between the rewritten prefix (system + summary)
// is byte-identical, which also keeps the prompt cache warm.
package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/adoonaai/shorttok/internal/anthropic"
	"github.com/adoonaai/shorttok/internal/pipeline"
	"github.com/adoonaai/shorttok/internal/tokens"
)

// Bump when the summary prompt changes, to invalidate cached summaries.
const promptVersion = "v1"

const systemPrompt = `You compress conversation history for an AI assistant that will continue the conversation.
Write a dense summary that preserves everything needed to continue: the user's goals, constraints and preferences, decisions made, facts, names, numbers, code identifiers, file paths, errors, open questions and anything the assistant promised to do.
Drop greetings, filler and repetition. Do not add commentary or advice.
Write in the language the conversation is held in.
If a previous summary is given, merge it with the new messages into one updated summary.`

// Summarizer is the side model used to write summaries.
type Summarizer interface {
	Complete(ctx context.Context, hdr http.Header, model, system, prompt string, maxTokens int) (string, anthropic.Usage, error)
}

type Config struct {
	Model            string // cheap model for summaries
	KeepLast         int    // messages always kept verbatim
	Chunk            int    // cut-point alignment, in messages
	MinMessages      int    // do nothing for shorter conversations
	MinTokens        int    // do nothing if the old part is smaller than this (est.)
	MaxSummaryTokens int
}

type Optimizer struct {
	cfg   Config
	sum   Summarizer
	store Store
}

func New(cfg Config, sum Summarizer, store Store) *Optimizer {
	return &Optimizer{cfg: cfg, sum: sum, store: store}
}

func (o *Optimizer) Name() string { return "history" }

func (o *Optimizer) Apply(ctx context.Context, rc *pipeline.Context) error {
	msgs := rc.Req.Messages
	if len(msgs) < o.cfg.MinMessages {
		return nil
	}
	k := SplitPoint(msgs, o.cfg.KeepLast, o.cfg.Chunk)
	if k == 0 || tokens.EstimateMessages(msgs[:k]) < o.cfg.MinTokens {
		return nil
	}

	hashes := prefixHashes(o.seed(rc.Headers), msgs[:k])

	// Longest already-summarized prefix.
	summary, from := "", 0
	for j := k; j > 0; j-- {
		if s, ok := o.store.Get(hashes[j]); ok {
			summary, from = s, j
			break
		}
	}

	if from < k {
		s, usage, err := o.sum.Complete(ctx, rc.Headers, o.cfg.Model, systemPrompt,
			buildPrompt(summary, msgs[from:k]), o.cfg.MaxSummaryTokens)
		rc.Report.AddAux(usage)
		if err != nil {
			return fmt.Errorf("summarize: %w", err)
		}
		if s = strings.TrimSpace(s); s == "" {
			return errors.New("summarize: empty summary")
		}
		summary = s
		o.store.Put(hashes[k], summary)
	}

	if err := rc.Req.AppendSystem(anthropic.TextBlock(wrap(summary))); err != nil {
		return err
	}
	rc.Req.Messages = msgs[k:]
	return nil
}

// SplitPoint returns how many leading messages to fold into the summary, or 0.
// The cut is aligned down to chunk and then moved back until the kept tail
// starts with a plain user message: the API requires the first message to be
// from the user, and a tool_use must never be separated from its tool_result.
func SplitPoint(msgs []anthropic.Message, keepLast, chunk int) int {
	k := len(msgs) - keepLast
	if k <= 0 {
		return 0
	}
	if chunk > 1 {
		k -= k % chunk
	}
	for k > 0 && !startsTurn(msgs[k]) {
		k--
	}
	return k
}

func startsTurn(m anthropic.Message) bool {
	if m.Role != "user" {
		return false
	}
	blocks, err := m.Blocks()
	if err != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type() == "tool_result" {
			return false
		}
	}
	return true
}

// seed isolates cached summaries per API key and per summary settings.
func (o *Optimizer) seed(h http.Header) string {
	cred := h.Get("x-api-key") + h.Get("authorization")
	sum := sha256.Sum256([]byte(cred))
	return promptVersion + "|" + o.cfg.Model + "|" + hex.EncodeToString(sum[:])
}

// prefixHashes returns h where h[i] identifies msgs[:i] (h[0] is the seed).
func prefixHashes(seed string, msgs []anthropic.Message) []string {
	out := make([]string, len(msgs)+1)
	h := sha256.Sum256([]byte(seed))
	out[0] = hex.EncodeToString(h[:])
	var buf bytes.Buffer
	for i, m := range msgs {
		buf.Reset()
		buf.Write(h[:])
		buf.WriteString(m.Role)
		buf.WriteByte(0)
		if err := json.Compact(&buf, m.Content); err != nil { // normalize whitespace
			buf.Write(m.Content)
		}
		h = sha256.Sum256(buf.Bytes())
		out[i+1] = hex.EncodeToString(h[:])
	}
	return out
}

func buildPrompt(prev string, msgs []anthropic.Message) string {
	var sb strings.Builder
	if prev != "" {
		sb.WriteString("<previous_summary>\n")
		sb.WriteString(prev)
		sb.WriteString("\n</previous_summary>\n\n")
	}
	sb.WriteString("<new_messages>\n")
	sb.WriteString(render(msgs))
	sb.WriteString("</new_messages>\n\nWrite the updated summary.")
	return sb.String()
}

func render(msgs []anthropic.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}
		blocks, err := m.Blocks()
		if err != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type() {
			case "text":
				fmt.Fprintf(&sb, "%s: %s\n\n", role, b.Text())
			case "tool_use":
				fmt.Fprintf(&sb, "%s called tool %q with input: %s\n\n", role, b.Str("name"), clip(string(b["input"]), 2000))
			case "tool_result":
				fmt.Fprintf(&sb, "Tool result: %s\n\n", clip(toolResultText(b), 2000))
			case "thinking", "redacted_thinking":
				// internal reasoning is not part of the dialogue
			default:
				fmt.Fprintf(&sb, "%s: [%s]\n\n", role, b.Type())
			}
		}
	}
	return sb.String()
}

func toolResultText(b anthropic.Block) string {
	inner, err := anthropic.ParseBlocks(b["content"])
	if err != nil {
		return string(b["content"])
	}
	parts := make([]string, 0, len(inner))
	for _, ib := range inner {
		if ib.Type() == "text" {
			parts = append(parts, ib.Text())
		} else {
			parts = append(parts, "["+ib.Type()+"]")
		}
	}
	return strings.Join(parts, "\n")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …[truncated]"
}

func wrap(summary string) string {
	return "The earlier part of this conversation was condensed by a context optimizer. " +
		"Treat this summary as what was said before the messages that follow:\n" +
		"<conversation_summary>\n" + summary + "\n</conversation_summary>"
}
