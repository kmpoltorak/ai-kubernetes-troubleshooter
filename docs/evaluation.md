# Evaluate diagnostic results

## Offline checks

Run the deterministic checks without credentials, a cluster or a model:

```bash
go test -race -count=1 -run '^TestEvaluate' -v ./internal/investigation
```

`make test` and CI include these tests. They use synthetic clusters and model
responses to check the rules, citations, missing-data handling and read-only
output. Passing them does not measure real-model accuracy, cost or resistance
to instructions embedded in workload data.

## Run a model evaluation

The opt-in tool sends synthetic evidence through the application's provider
client and report validator. It needs no database, cluster or UI and does not
read `.env`, kubeconfig or incident files. Choose an explicit model compatible
with schema-constrained chat requests and temperature zero (or a reasoning model
with `MODEL_EVAL_REASONING_EFFORT`), and review its cost
before running. There are no automatic retries or redirects.

| Setting | Value |
|---|---|
| `MODEL_EVAL_PROVIDER` | required: `openai`, `anthropic` or `ollama` |
| `MODEL_EVAL_MODEL` | required; no default |
| `MODEL_EVAL_ALLOW_NETWORK` | must equal `true`, including for local Ollama |
| `MODEL_EVAL_REASONING_EFFORT` | OpenAI or Anthropic, e.g. `medium`; OpenAI gets `reasoning_effort` instead of temperature 0, which reasoning models reject (recorded as `temperature: null`); Anthropic gets `output_config.effort` and never a temperature |
| `OPENAI_API_KEY` | environment only; required for OpenAI |
| `OPENAI_BASE_URL` | default `https://api.openai.com/v1` |
| `ANTHROPIC_API_KEY` | environment only; required for Anthropic |
| `ANTHROPIC_BASE_URL` | default `https://api.anthropic.com` |
| `OLLAMA_BASE_URL` | default `http://localhost:11434` |
| Endpoint | HTTPS, or HTTP loopback; no URL credentials, query or fragment |
| Attempts | six fixed cases × three attempts, serially; at most 18 requests |
| Time | 60 seconds per request; 20 minutes for the suite |
| Input | 262144 serialized request bytes, including schema |
| Generation | at most 4096 tokens requested per attempt |

Limits are fixed; application `LLM_*` settings do not change them. The byte
limit does not ensure a model context fit. Request/token caps are not a currency
budget. Interrupted calls may be billed, and missing usage does not mean zero.

For Ollama, set the exact model name in `MODEL_EVAL_MODEL`, then run:

```bash
: "${MODEL_EVAL_MODEL:?Set an explicit model first}"
MODEL_EVAL_ALLOW_NETWORK=true MODEL_EVAL_PROVIDER=ollama \
  go test -tags modeleval -run '^TestEvaluateModel$' -count=1 -timeout=22m -json \
  ./internal/investigation > report-model-evaluation.jsonl
```

For OpenAI or Anthropic, set `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` in the
environment and use `MODEL_EVAL_PROVIDER=openai` or `anthropic`. The 4096-token
limit includes Anthropic thinking; a cut-off report fails the attempt. Keep keys out of arguments and output files.
Choose a new output filename each time: `>` overwrites an existing file.
Rerunning starts another set of calls; it does not resume failed attempts.

## Read the results

Keep the exit status and complete Go test JSON, including failures. Extract
records by joining Go output fragments:

```bash
jq -cs '[.[] | select(.Action == "output") | .Output] | join("") | split("\n")[] | fromjson?' \
  report-model-evaluation.jsonl
```

Each attempt records case/repeat, evidence IDs, requested/reported model, request
settings, prompt/schema hashes, timing, provider usage, decoded model analysis
and the engine's final analysis. Timings are nanoseconds; absent counters are
`null`. OpenAI and Anthropic report token usage; Ollama also reports generation/load timings.
See the [OpenAI response fields](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create),
[Anthropic Messages API](https://platform.claude.com/docs/en/api/messages)
and [Ollama response fields](https://docs.ollama.com/api/chat).

Transport, decoding and validation failures remain failed attempts. Usage can
still be available for a rejected report. Raw HTTP responses and undecodable
model text are omitted; `error_class` describes the failure. Output is masked,
which does not guarantee removal of every encoded secret. Use synthetic data.
Cancellation or an output error stops further calls; partial output without a
summary is incomplete.

`automated_passed` covers structural checks only. Human review remains `pending`
and `qualified` remains `false`. Review all 18 attempts against their evidence:
factual support, observation versus hypothesis, disclosed gaps and useful
read-only follow-up. Do not count engine-added context as model-authored.
Keep failures in the assessment; three repetitions across six synthetic cases
are not a statistical accuracy estimate or a production benchmark.
