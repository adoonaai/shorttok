// Command shorttok-eval generates eval datasets and measures the proxy:
// how much money it saves and how much answer quality it costs.
//
//	shorttok-eval gen -n 20 -o testdata/eval/needle.jsonl
//	shorttok-eval run -dataset testdata/eval/needle.jsonl -o eval-report.md
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/adoonaai/shorttok/internal/eval"
	"github.com/adoonaai/shorttok/internal/pricing"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = gen(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shorttok-eval gen|run [flags]  (use -h for flags)")
	os.Exit(2)
}

func gen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	n := fs.Int("n", 20, "number of conversations")
	filler := fs.Int("filler", 12, "filler turns between the facts and the question")
	model := fs.String("model", "claude-haiku-4-5-20251001", "model to evaluate")
	seed := fs.Int64("seed", 42, "random seed")
	out := fs.String("o", "testdata/eval/needle.jsonl", "output file")
	_ = fs.Parse(args)

	if err := os.MkdirAll(dir(*out), 0o755); err != nil {
		return err
	}
	if err := eval.Save(*out, eval.Generate(*n, *filler, *model, *seed)); err != nil {
		return err
	}
	fmt.Printf("wrote %d conversations to %s\n", *n, *out)
	return nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	proxy := fs.String("proxy", "http://localhost:8080", "ShortTok base URL")
	dataset := fs.String("dataset", "testdata/eval/needle.jsonl", "JSONL dataset")
	judge := fs.String("judge-model", "claude-sonnet-5", "judge model; empty disables the judge")
	replay := fs.Bool("replay", true, "replay conversations turn by turn like a real chat")
	conc := fs.Int("c", 4, "conversations in parallel")
	limit := fs.Int("limit", 0, "evaluate only the first N conversations")
	pricingFile := fs.String("pricing", "", "pricing JSON to merge over defaults")
	out := fs.String("o", "eval-report.md", "markdown report")
	_ = fs.Parse(args)

	items, err := eval.Load(*dataset)
	if err != nil {
		return err
	}
	if *limit > 0 && *limit < len(items) {
		items = items[:*limit]
	}
	prices := pricing.Default()
	if *pricingFile != "" {
		if err := prices.LoadFile(*pricingFile); err != nil {
			return err
		}
	}
	for _, it := range items {
		if _, ok := prices.Lookup(it.Model); !ok {
			fmt.Fprintf(os.Stderr, "warning: no price for %s, costs will show $0 (use -pricing)\n", it.Model)
			break
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	r := &eval.Runner{
		ProxyURL:    *proxy,
		APIKey:      os.Getenv("ANTHROPIC_API_KEY"),
		HTTP:        &http.Client{Timeout: 5 * time.Minute},
		Pricing:     prices,
		JudgeModel:  *judge,
		Replay:      *replay,
		Concurrency: *conc,
		Progress: func(done, total int, res eval.Result) {
			status := fmt.Sprintf("recall %.0f%%→%.0f%% score %d", 100*res.Base.Recall, 100*res.Opt.Recall, res.Score)
			if res.Err != nil {
				status = "ERROR " + res.Err.Error()
			}
			fmt.Printf("[%d/%d] %s %s\n", done, total, res.ID, status)
		},
	}
	results := r.Run(ctx, items)
	sum := eval.Summarize(results)

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	eval.WriteMarkdown(f, results, sum)
	if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("\nsaved %.1f%% ($%.4f → $%.4f, summaries $%.4f)", sum.SavedPct(),
		float64(sum.BaseMicro)/1e6, float64(sum.OptMicro)/1e6, float64(sum.AuxMicro)/1e6)
	if sum.BaseRecall >= 0 {
		fmt.Printf(" · recall %.1f%% → %.1f%%", 100*sum.BaseRecall, 100*sum.OptRecall)
	}
	if sum.Judged > 0 {
		fmt.Printf(" · judge %.2f/5", sum.AvgScore)
	}
	fmt.Printf("\nreport: %s\n", *out)
	if sum.Failed > 0 {
		return fmt.Errorf("%d of %d conversations failed", sum.Failed, sum.Items)
	}
	return nil
}

func dir(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}
