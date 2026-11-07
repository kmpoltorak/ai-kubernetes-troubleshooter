# Limitations

## Access and data

- Authentication is off by default for local and test use. Enable it before
  exposing incident data; all admitted users share the installation's incidents,
  with no per-user or per-namespace roles.
- With an API provider (OpenAI, Anthropic or Gemini), masked incident text and
  diagnostic data are sent to that provider.
- Redaction uses patterns and does not guarantee removing every secret;
  identifiers remain visible. Base64 lines and similar blobs may also be masked.
  Older rows and backups are not rewritten; API reads mask older data.
- Exports are created in the browser; the server does not record who exported what.

## Diagnostics

- Custom resources are judged by the [condition names listed in configuration](configuration.md#custom-resources);
  a controller using other names is not interpreted, and a resource without
  conditions or phase is `inconclusive`. Pods are found only through a label
  selector, not owner references. A configured resource the cluster does not
  serve fails its investigations.
- A CronJob is judged by its latest Job only (found among at most 500 Jobs in the
  namespace); missed schedules are not detected.
- An Ingress is followed to its first existing backend's pods; an autoscaler only
  to a Deployment or StatefulSet.
- A Pending claim no pod uses is not called a fault (WaitForFirstConsumer classes
  bind later); StorageClasses are not read.
- Nodes are read only when they run the target's pods, so a Pending pod's
  candidate nodes are not inspected.
- Empty observations count as missing data: no log lines (core) or no usage
  samples (optional). A healthy workload whose containers write no logs is
  therefore `inconclusive`, not `no_fault_observed`.
- No remediation is executed.

## Analysis

- Citation validation checks that cited evidence exists in the attempt, not the
  truth of model prose. Model-written reports must be verified.
- An interrupted LLM request is never automatically repeated; its outcome can be
  unknown and billed without a stored report.
- The LLM input limit counts JSON bytes, not tokens; it does not guarantee a
  model context fit.

## Clusters and replicas

- Clusters of one installation share custom resources, limits and analyzer;
  replicas must use the same limits, target policy and analyzer.
- Kubeconfig changes, including a renewed token, need a restart.

## Browser UI

- The UI needs browser session storage for safe submission retries and a browser
  supporting `AbortSignal.timeout`/`any`; the "?" hints need the Popover API
  (Chrome 114, Safari 17, Firefox 125).
- It watches one job at a time; jobs from other sessions are discovered on
  loaded history pages.
- Accessibility is checked with axe's automated rules in Chromium only; there is
  no manual screen-reader audit.

## Tested scope

- Live CI uses single-node kind on Kubernetes 1.37.0, Linux amd64; other
  Kubernetes versions are untested. Other CNI implementations, clouds and cluster
  OIDC/TLS/Ingress are not covered.
- The OpenAI, Anthropic and Gemini clients use fake servers in CI; two models per
  provider were run against the real APIs on 18 synthetic attempts, with human
  review pending ([results](evaluation.md#measured-models)).
- Browser tests run in Chromium only; Firefox and Safari are untested.

## Upgrades

- Upgrades are tested from commit `06db1ed` (schema 1) and from the newest
  published release, built from its tag's source (not its image).
- The application refuses to start on a newer database schema; roll back by
  restoring the pre-upgrade backup. Builds before this check lack it.
- After v2 reports exist, schema downgrade is blocked; see
  [upgrade and rollback](analysis-contract.md#upgrade-and-rollback).
- Legacy `running` rows are not automatically repaired.
