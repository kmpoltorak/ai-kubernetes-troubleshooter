# Report format

New analyses have `report.analysis.schema_version: 2`. The job, history and latest
report endpoints all return this shape. Missing `schema_version` identifies a
legacy report. The legacy `confidence` field is absent from new analyses; it is
not replaced with a number derived from evidence support.

## Assessment and support

| Field | Meaning |
|---|---|
| `summary`, `root_cause` | explanation and concise assessment; the UI labels the latter Assessment |
| `verdict` | `observed_fault`, `suspected_fault`, `no_fault_observed`, `inconclusive` |
| `support` | `strong`, `limited`, `insufficient`: qualitative support for this assessment, not probability |
| `evidence` | observations citing `evidence_id`, matching `source`, and `description` |
| `missing_data` | errors from diagnostics plus gaps identified by the analyzer |
| `limitations` | coverage/sampling limits from the application plus analyzer qualifications |
| `severity`, `affected_resources` | impact and resources, as before |
| `possible_causes` | hypotheses; separate from observed evidence |
| `recommended_actions`, `safe_commands` | recommendations requiring human review and validated read-only diagnostic commands |

`observed_fault` needs a cited unhealthy observation. It can use `strong` support
for the narrow condition actually observed. For example, OOMKilled establishes
that reported state; it does not establish a memory leak or prove an undersized
container limit. CrashLoopBackOff and an unschedulable pod similarly do not by
themselves establish the underlying application or placement cause.

`strong` is forbidden for other verdicts. `inconclusive` requires `insufficient`.
`no_fault_observed` means no fault was identified in these bounded snapshots;
it cannot establish complete health. The application rejects it when any
collected observation is unhealthy, or namespace/target, pod, Service, event or
log diagnostics failed. Optional metric failure is retained as missing data.

These checks enforce structure and consistency. They do not prove the truth of
model prose, or that a cited observation supports every claim in its description.
The built-in rules are deterministic; external models can still misinterpret data.

## Exact citations

```json
{
  "evidence_id": "ee376713-5c52-48d9-9ded-95113daaab0c",
  "source": "get_pods",
  "description": "The sampled pod reports OOMKilled."
}
```

The UUID must identify evidence collected in the current investigation attempt,
and the source must match that evidence. Another attempt's ID is rejected even
when it uses the same diagnostic tool. The analyzer receives evidence IDs; it
does not need incident or job IDs. An invalid result fails the attempt and keeps
its diagnostic evidence; it does not trigger another automatic LLM request.

UI citations link to the exact diagnostic row within the selected attempt.
Legacy reports retain tool-level citations and remain readable, with a legacy
label. Their historical numeric score remains in the API; the UI does not show
it as a percentage or convert it into a v2 support level.

## Missing data and sampling

The application adds failed diagnostic messages and configured sampling bounds
even if the analyzer omits them. It masks these fields before storage and API
output. Prompts include the same coverage information within the existing whole
request byte limit. Empty collections mean none reported, not complete coverage.

Sampling caps are maxima, not a claim that every cap was reached: up to 20 pod
details, 50 related events, three pods' log reads, five references per ConfigMap
or Secret kind and 21 event subjects. Log requests are capped at 200 tail lines
and 32 KiB per stream; retention keeps 30 tail lines plus up to 20 error lines,
each capped at 300 bytes. The existing log `truncated` marker is reported when
present. Observations are not simultaneous; events are matched by name, not UID.

NetworkPolicy reports describe declarations matching the supplied labels. They
do not observe CNI enforcement or the actual connection path. Selecting policies
combine their allows; an empty policy cannot override another matching allow.
See [Kubernetes NetworkPolicy semantics](https://kubernetes.io/docs/concepts/services-networking/network-policies/).
The diagnostic detects matching allow-all and combined empty/narrow declarations;
it does not emulate every effective flow. A timeout alongside restrictive
policies is a hypothesis, with dependency and routing failures among alternatives.
Verify the actual destination, combined policies and CNI before changing policy.

## Upgrade and rollback

Back up PostgreSQL and stop old application processes before upgrading. Startup
applies migration 0003, making the historical `reports.confidence` column nullable.
V2 reports store SQL NULL there and omit the JSON field. Existing reports and
scores are not rewritten, backfilled or revalidated on read.

Clients consuming new reports must understand schema v2, verdict and support;
old clients that require `confidence` need updating. The bundled UI handles both
versions. Do not run old and new writers together: old writers emit legacy reports.

`api migrate down` takes a table lock and refuses if v2 reports or NULL scores
exist. It rolls back without deleting reports, synthesizing scores or changing
the migration version. Once v2 reports exist, returning to the old contract needs
a compatible backup or a separately reviewed migration. Restoring a backup can
lose newer data; follow your backup policy. With only compatible legacy rows,
the down-migration restores the old NOT NULL constraint.
