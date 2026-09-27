package eval

import (
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/andrey/shorttok/internal/anthropic"
)

// Generate builds synthetic "needle" conversations: facts are stated in the
// first turns, followed by unrelated filler turns, and the final question asks
// to recall them. The facts land in the part the history optimizer
// summarizes, so recall shows directly whether summaries lose information.
//
// This is a smoke test. For real numbers, export conversations from your own
// application logs into the same JSONL format.
func Generate(n, fillerTurns int, model string, seed int64) []Item {
	rng := rand.New(rand.NewSource(seed))
	pick := func(xs []string) string { return xs[rng.Intn(len(xs))] }

	items := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		region, db, budget, deadline, lead := pick(regions), pick(databases), pick(budgets), pick(deadlines), pick(leads)

		var msgs []anthropic.Message
		add := func(role, text string) { msgs = append(msgs, msg(role, text)) }

		add("user", fmt.Sprintf("I'm designing a new backend service. The servers will run in %s and we have settled on %s as the main database.", region, db))
		add("assistant", pick(acks))
		add("user", fmt.Sprintf("Infrastructure budget is %s per month, and the release deadline is %s.", budget, deadline))
		add("assistant", pick(acks))
		add("user", fmt.Sprintf("%s is the team lead and approves every architecture decision.", lead))
		add("assistant", pick(acks))

		for _, j := range rng.Perm(len(fillers))[:min(fillerTurns, len(fillers))] {
			add("user", fillers[j].q)
			add("assistant", fillers[j].a)
		}

		add("user", "Let's wrap up. Remind me of the key constraints: the region, the database, the monthly budget, "+
			"the deadline and who approves decisions. Then give a one-paragraph rollout plan that respects them.")

		items = append(items, Item{
			ID:        fmt.Sprintf("needle-%03d", i+1),
			Model:     model,
			MaxTokens: 600,
			System:    "You are a pragmatic senior backend engineer helping a colleague.",
			Messages:  msgs,
			Expect:    []string{region, db, budget, deadline, lead},
		})
	}
	return items
}

func msg(role, text string) anthropic.Message {
	c, _ := json.Marshal(text)
	return anthropic.Message{Role: role, Content: c}
}

var (
	regions   = []string{"Frankfurt", "Helsinki", "Amsterdam", "Warsaw", "Singapore", "Stockholm"}
	databases = []string{"PostgreSQL 16", "MySQL 8", "CockroachDB", "MongoDB 7", "ClickHouse"}
	budgets   = []string{"$180", "$240", "$95", "$310", "$450"}
	deadlines = []string{"March 14", "June 2", "October 30", "August 19", "December 5"}
	leads     = []string{"Irina", "Pavel", "Sofia", "Timur", "Arseniy"}
	acks      = []string{
		"Noted. What else should I keep in mind?",
		"Got it, I'll take that into account. Anything else?",
		"Understood. Go on.",
	}
)

type qa struct{ q, a string }

var fillers = []qa{
	{"How should I structure logging in a Go service?",
		"Use structured logging from day one: log/slog in the standard library is enough for most services. Emit JSON in production so your log pipeline can index fields, and a text handler locally. Attach request-scoped fields such as request ID, user ID and route through a logger stored in the context rather than formatting them into messages. Log at the edges: incoming requests, outgoing calls and errors, not every function. Keep levels meaningful: errors are things someone must act on, warnings are degraded but handled states, info is lifecycle and business events, and debug is disabled by default."},
	{"What's a sane approach to configuration?",
		"Read configuration once at startup into a typed struct and validate it immediately, failing fast with a clear message listing every invalid field. Environment variables work well for containers; a file is useful when the config gets nested. Keep secrets out of files committed to the repository and inject them at runtime. Give every option a sensible default so the service starts locally without ceremony, and print the effective configuration, minus secrets, at startup. Avoid global mutable config: pass the struct, or the parts each component needs, explicitly through constructors."},
	{"How do I handle graceful shutdown?",
		"Listen for SIGINT and SIGTERM with signal.NotifyContext, then call Shutdown on the HTTP server with a timeout so in-flight requests can finish. Stop accepting new work first, drain queues and background workers next, and close database pools and other resources last. Make readiness probes fail as soon as shutdown begins, so the load balancer stops routing traffic before the process exits. Keep the total shutdown budget below the orchestrator's termination grace period, otherwise the process is killed mid-drain."},
	{"Any advice on retries for outgoing HTTP calls?",
		"Retry only idempotent operations, or make them idempotent with request keys. Use exponential backoff with jitter to avoid synchronized retry storms, cap both the number of attempts and the total time, and respect Retry-After headers. Retry on timeouts, 429 and 5xx responses, never on other 4xx errors. Put a circuit breaker in front of flaky dependencies so a failing service doesn't drag yours down, and always propagate the caller's context so cancelled requests stop retrying immediately."},
	{"How should I test database code?",
		"Prefer real databases over mocks for repository code: spin up the same engine in a container, apply migrations, and run tests against it. Keep each test isolated with a transaction that is rolled back, or with unique schemas. Mocks are fine at a higher layer, for business logic that depends on a repository interface. Seed data through helpers that build valid objects with sensible defaults, so tests only state what matters to them. Run the suite in CI on every pull request; slow integration tests belong in a separate job, not in the fast unit loop."},
	{"What metrics should every service expose?",
		"Start with the RED method for request-driven services: rate, errors and duration, broken down by route and status class. Add saturation signals for the resources you depend on, such as connection pool usage, queue depth and goroutine count. Expose them in Prometheus format and keep label cardinality bounded: never put user IDs or raw URLs into labels. Pair metrics with alerts on symptoms users feel, like error rate and latency objectives, rather than on every internal fluctuation."},
	{"How do I version an internal API?",
		"Prefer additive, backward-compatible changes: new optional fields, new endpoints, tolerant readers that ignore unknown fields. When a breaking change is unavoidable, introduce a new version alongside the old one, migrate clients, watch traffic on the old version drop to zero, and only then remove it. Document deprecations with dates and communicate them early. Contract tests between producer and consumers catch accidental breaks before they reach production."},
	{"How do you approach code reviews?",
		"Keep pull requests small and focused so they can be reviewed in one sitting. The author explains the why in the description and points out the risky parts. Reviewers look at correctness, readability and failure modes before style; formatting and lint issues should be automated away. Comments are phrased as questions or suggestions, and blocking concerns are marked explicitly. Aim to respond within a working day, because long review queues cost more than imperfect code."},
	{"What's your take on feature flags?",
		"Feature flags decouple deployment from release: ship code dark, enable it for internal users, then a percentage of traffic, and roll back by flipping a switch instead of redeploying. Treat flags as temporary and give each an owner and an expiry date, otherwise they pile up into untestable combinations. Evaluate them through a small interface so the flag provider can be swapped, and log flag states with requests to make incidents easier to debug."},
	{"How should errors be handled in Go?",
		"Return errors, wrap them with context using fmt.Errorf and %w, and handle each error exactly once: either log it or return it, not both. Use errors.Is and errors.As for decisions instead of comparing strings. Define sentinel errors or typed errors only for conditions callers genuinely branch on. At the service boundary, map internal errors to stable external codes and messages, and never leak stack traces or SQL to clients."},
	{"Any tips for writing a good README?",
		"Lead with one sentence on what the project does and who it is for, then a quick start that works when copied verbatim. Show a minimal example of usage, list configuration in a table, and link to deeper documentation instead of inlining everything. Include how to run tests and how to contribute. A diagram of the architecture helps reviewers orient quickly. Keep it current: an outdated README is worse than a short one."},
	{"How do you plan capacity for a new service?",
		"Estimate peak requests per second from product expectations, multiply by a safety factor, and load test a single instance to find its saturation point. Divide to get the instance count, then add headroom for failures and deploys. Watch the database separately: it is usually the first bottleneck, so check connection limits and slow queries under load. Revisit the numbers after launch with real traffic instead of trusting the initial guess."},
}
