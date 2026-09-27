package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/adoonaai/shorttok/internal/anthropic"
)

const judgeSystem = `You grade an AI assistant's answer against a reference answer written for the same conversation.
Score the candidate from 1 to 5:
5 - same facts and equally useful
4 - minor omissions or imprecision
3 - some facts missing or vague
2 - important facts wrong or missing
1 - unusable
Judge factual agreement and usefulness only, not wording, style or length.
Reply with JSON only: {"score": <1-5>, "reason": "<one sentence>"}`

// judge compares the optimized answer with the baseline one. The judge call
// itself goes through the proxy with bypass, so it is never optimized.
func (r *Runner) judge(ctx context.Context, it Item, reference, candidate string) (int, string, error) {
	question := ""
	if blocks, err := it.Messages[len(it.Messages)-1].Blocks(); err == nil {
		for _, b := range blocks {
			question += b.Text()
		}
	}
	prompt := fmt.Sprintf("<question>\n%s\n</question>\n\n<reference>\n%s\n</reference>\n\n<candidate>\n%s\n</candidate>",
		question, reference, candidate)

	c, err := r.call(ctx, r.JudgeModel, judgeSystem, []anthropic.Message{msg("user", prompt)}, 300, true)
	if err != nil {
		return 0, "", err
	}
	return parseJudge(c.text)
}

func parseJudge(text string) (int, string, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return 0, "", fmt.Errorf("no JSON in judge reply: %.100q", text)
	}
	var v struct {
		Score  int    `json:"score"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return 0, "", err
	}
	if v.Score < 1 || v.Score > 5 {
		return 0, "", fmt.Errorf("score out of range: %d", v.Score)
	}
	return v.Score, v.Reason, nil
}
