package eval

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type Summary struct {
	Items, Failed, Judged         int
	BaseMicro, OptMicro, AuxMicro int
	BaseRecall, OptRecall         float64 // -1 if no item has expectations
	AvgScore                      float64
	Scores                        [6]int // index = score
	BaseP50, OptP50               time.Duration
}

func (s Summary) SavedPct() float64 {
	if s.BaseMicro == 0 {
		return 0
	}
	return 100 * float64(s.BaseMicro-s.OptMicro) / float64(s.BaseMicro)
}

func Summarize(results []Result) Summary {
	var (
		s                    Summary
		recallN              int
		baseRec, optRec, sum float64
		baseLat, optLat      []time.Duration
	)
	s.BaseRecall, s.OptRecall = -1, -1
	for _, r := range results {
		s.Items++
		if r.Err != nil {
			s.Failed++
			continue
		}
		s.BaseMicro += r.Base.TotalMicro()
		s.OptMicro += r.Opt.TotalMicro()
		s.AuxMicro += r.Opt.AuxMicro
		baseLat = append(baseLat, r.Base.Latency)
		optLat = append(optLat, r.Opt.Latency)
		if r.Base.Recall >= 0 {
			recallN++
			baseRec += r.Base.Recall
			optRec += r.Opt.Recall
		}
		if r.Score > 0 {
			s.Judged++
			s.Scores[r.Score]++
			sum += float64(r.Score)
		}
	}
	if recallN > 0 {
		s.BaseRecall, s.OptRecall = baseRec/float64(recallN), optRec/float64(recallN)
	}
	if s.Judged > 0 {
		s.AvgScore = sum / float64(s.Judged)
	}
	s.BaseP50, s.OptP50 = median(baseLat), median(optLat)
	return s
}

func WriteMarkdown(w io.Writer, results []Result, s Summary) {
	fmt.Fprintf(w, "# ShortTok eval report\n\n")
	fmt.Fprintf(w, "Conversations: %d (failed: %d), judged: %d\n\n", s.Items, s.Failed, s.Judged)
	fmt.Fprintf(w, "| | Baseline | Optimized |\n|---|---|---|\n")
	fmt.Fprintf(w, "| Cost, USD | %s | %s (incl. summaries %s) |\n", usd(s.BaseMicro), usd(s.OptMicro), usd(s.AuxMicro))
	if s.BaseRecall >= 0 {
		fmt.Fprintf(w, "| Fact recall | %.1f%% | %.1f%% |\n", 100*s.BaseRecall, 100*s.OptRecall)
	}
	fmt.Fprintf(w, "| Final answer latency, p50 | %s | %s |\n\n", s.BaseP50.Round(time.Millisecond), s.OptP50.Round(time.Millisecond))
	fmt.Fprintf(w, "**Saved: %.1f%%**", s.SavedPct())
	if s.Judged > 0 {
		fmt.Fprintf(w, " · judge score %.2f / 5 · scores 5/4/3/2/1: %d/%d/%d/%d/%d",
			s.AvgScore, s.Scores[5], s.Scores[4], s.Scores[3], s.Scores[2], s.Scores[1])
	}
	fmt.Fprintf(w, "\n\n## Conversations\n\n")
	fmt.Fprintf(w, "| ID | Calls | Base USD | Opt USD | Saved | Recall base/opt | Score | Judge |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(w, "| %s | – | – | – | – | – | – | error: %s |\n", r.ID, cell(r.Err.Error()))
			continue
		}
		saved := 0.0
		if b := r.Base.TotalMicro(); b > 0 {
			saved = 100 * float64(b-r.Opt.TotalMicro()) / float64(b)
		}
		recall := "–"
		if r.Base.Recall >= 0 {
			recall = fmt.Sprintf("%.0f%% / %.0f%%", 100*r.Base.Recall, 100*r.Opt.Recall)
		}
		fmt.Fprintf(w, "| %s | %d | %s | %s | %.1f%% | %s | %d | %s |\n",
			r.ID, r.Opt.Calls, usd(r.Base.TotalMicro()), usd(r.Opt.TotalMicro()), saved, recall, r.Score, cell(r.JudgeReason))
	}
}

func usd(micro int) string { return fmt.Sprintf("$%.4f", float64(micro)/1e6) }

func cell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

func median(xs []time.Duration) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	return xs[len(xs)/2]
}
