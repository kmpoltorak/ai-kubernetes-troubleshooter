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
read `.env`, kubeconfig or incident files. Choose an explicit model that
supports schema-constrained output and reasoning effort, and review its cost
before running. There are no automatic retries or redirects.

| Setting | Value |
|---|---|
| `MODEL_EVAL_PROVIDER` | required: `openai`, `anthropic` or `gemini` |
| `MODEL_EVAL_MODEL` | required; no default |
| `MODEL_EVAL_ALLOW_NETWORK` | must equal `true` |
| `MODEL_EVAL_TIMEOUT_SECONDS` | default `300`, at most `600`; per request, recorded as `request_timeout_seconds` |
| `MODEL_EVAL_REASONING_EFFORT` | default `medium`; sent as OpenAI `reasoning_effort`, Anthropic `output_config.effort` or Gemini `thinkingLevel`; `off` sends none (OpenAI then gets temperature 0) |
| `OPENAI_API_KEY` | environment only; required for OpenAI |
| `OPENAI_BASE_URL` | default `https://api.openai.com/v1` |
| `ANTHROPIC_API_KEY` | environment only; required for Anthropic |
| `ANTHROPIC_BASE_URL` | default `https://api.anthropic.com` |
| `GEMINI_API_KEY` | environment only; required for Gemini |
| `GEMINI_BASE_URL` | default `https://generativelanguage.googleapis.com/v1beta` |
| Endpoint | HTTPS, or HTTP loopback; no URL credentials, query or fragment |
| Attempts | six fixed cases × three attempts, serially; at most 18 requests |
| Time | the request timeout; each investigation gets 30 seconds more, and the suite 18 requests plus 2 minutes (92 minutes at the 300-second default) |
| Input | 262144 serialized request bytes, including schema |
| Generation | at most 16000 tokens per attempt, thinking included (the application default) |

Other limits are fixed; application `LLM_*` settings do not change them. The byte
limit does not ensure a model context fit. Request/token caps are not a currency
budget. Interrupted calls may be billed, and missing usage does not mean zero.

Set `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` or `GEMINI_API_KEY` in the environment, choose the
provider and an exact model name, then run:

```bash
: "${MODEL_EVAL_MODEL:?Set an explicit model first}"
MODEL_EVAL_ALLOW_NETWORK=true MODEL_EVAL_PROVIDER=openai \
  go test -tags modeleval -run '^TestEvaluateModel$' -count=1 -timeout=95m -json \
  ./internal/investigation > report-model-evaluation.jsonl
```

`-timeout=95m` covers the suite at the 300-second default; for a longer
`MODEL_EVAL_TIMEOUT_SECONDS`, raise it above the suite limit. A report cut off at the token
limit fails the attempt. Keep keys out of
arguments and output files.
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
`null`. All three providers report token usage.
See the [OpenAI response fields](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create),
the [Anthropic Messages API](https://platform.claude.com/docs/en/api/messages)
and the [Gemini generateContent reference](https://ai.google.dev/api/generate-content).

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

## Local models

Local models through Ollama were measured with this tool and removed, because
none produced usable reports. Each ran the 18 attempts, or as many as the
results warranted, on a Ryzen with an Intel Arc GPU (10 GB), a 16k context and
a 300-second request timeout:

| Model | Accepted reports | Typical failure |
|---|---|---|
| `llama3.2:1b` | 0 of 18 | `no_fault_observed` for every case, including OOMKilled |
| `qwen2.5:3b` | 0 of 4 | output looped until the 4096-token limit |
| `phi4-mini` | 1 of 5 | evidence IDs that do not match their source |
| `qwen2.5:7b` | 9 of 18 | `no_fault_observed` although logs could not be read; a `kubectl get secret` command |

These runs used a 4096-token limit. The validator rejected the failing reports,
so they would not reach a user; some accepted ones still overstated a past
error as a suspected fault. Local requests took 40–290 seconds.
