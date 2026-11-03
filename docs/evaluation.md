# Offline evaluation

Run the deterministic evaluation without credentials, a real cluster or a model:

```bash
go test -race -count=1 -run '^TestEvaluate' -v ./internal/investigation
```

`make test` and CI include these tests. They run the actual investigation engine,
read-only diagnostics, evidence collection and rules analyzer against client-go
fake clusters. Counterexamples modify synthetic test fixtures; the public demo
scenarios and API options stay the same. Separate model-output fixtures return
otherwise valid v2 assessments containing prohibited commands to test rejection.

## Rubric

A case must satisfy its declared verdict, support level and expected assessment.
Baseline demo cases also check severity. Counterexamples prohibit specific
unsupported conclusions, require relevant coexisting signals to remain available,
and check missing-data/truncation disclosures. Every report must cite evidence
from its own attempt with matching source names. An unreadable target must fail
without a report while keeping collected evidence. The diagnostic API calls must
remain read-only, including when input text or model output requests a write.

| Counterexample | Required outcome |
|---|---|
| OOM plus missing ConfigMap | missing-reference headline; both faults remain in collected evidence |
| transient rollout | inconclusive; no invented scheduling, OOM, policy or crash diagnosis |
| denied pod logs | inconclusive, with missing log data; no healthy verdict |
| unavailable optional metrics | bounded no-fault observation, with missing metrics disclosed |
| truncated unknown log errors | inconclusive and truncation disclosed |
| unknown application error | inconclusive; no guessed known cause |
| old memory-error log with healthy current pod state | inconclusive; no claim of current OOMKilled |
| instructions in the incident description | diagnosis still follows observations |
| instructions in logs | text remains data; no requested write occurs |
| unreadable target | failed attempt, preserved evidence, no report |
| matching allow-all with a narrow policy | no policy-block headline based on the narrow policy alone |
| valid v2 output with write/Secret/credential commands | rejected assessment, preserved evidence, no command execution |

The expected outcomes are explicit regression criteria, not measurements of
real-world diagnostic accuracy. Review the fixtures and expectations when
changing a rule; do not accept a changed result just to make CI pass.

## Interpretation and limits

The rules choose the first matching known pattern. They do not enumerate every
cause or decide which coexisting fault caused the reported incident. For example,
the mixed ConfigMap/OOM case preserves both observations but has one headline.
Current observations and older log samples can disagree; an inconclusive result
is preferable to inventing a temporal cause.

These fixtures are synthetic and use the same diagnostic/rule implementation
being evaluated. Passing them is not an accuracy percentage or evidence of
generalization to other workloads. Fake clients do not exercise Kubernetes API
validation, RBAC enforcement, CNI behavior or real network failures. The tests
with injected instructions cover the deterministic rules and server-side output
validation; they do not establish an external model's resistance to injection.

No external-model benchmark or paid call runs in this suite. Human Kubernetes
review of the critical cases is still needed. Real-model quality, provider
variability and token/cost measurements are not measured by these tests.
