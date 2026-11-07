# Changelog

## [1.8.0](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.7.0...v1.8.0) (2026-11-07)


### Features

* run a published release image from Docker Compose with APP_IMAGE ([#115](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/115)) ([add39af](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/add39af574a538c6fdd86dbdf9ea7a7617263918))

## [1.7.0](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.6.0...v1.7.0) (2026-11-07)


### Features

* name a live cluster after its kubeconfig context instead of "local" ([#110](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/110)) ([9f15b3d](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/9f15b3dbcedb490acb3d040a4fa7f5aad91c95a0))

## [1.6.0](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.5.0...v1.6.0) (2026-11-07)


### Features

* add Gemini as an LLM provider ([#90](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/90)) ([ff17716](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/ff177165aa9aab3e55edadadab670a08e4978f57))
* Compose uses your PostgreSQL; the bundled one moves to a test-only override ([#109](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/109)) ([8db5fe8](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/8db5fe87dceb598794982bb91fe6f2e6381f181e))
* configurable output token limit and reasoning effort by default ([#93](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/93)) ([c274f6c](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/c274f6c7d849291dddd3442e37eb774e6ecaf6a5))
* default LLM timeout 300s, investigation 360s, checked against each other ([#96](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/96)) ([62d497b](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/62d497b9f64dd46b8a87d3a5494a4fb7c3eaf70a))
* drop the 1.x compatibility fields from /api/v1/runtime ([#108](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/108)) ([8464f77](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/8464f77cdac5dd797d6c58d7676c638225afd0e4))
* header shows mode and model once; Settings lists every non-secret setting ([#98](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/98)) ([52745c7](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/52745c72109b648fd37b2f20e9745cc893eb2da9))
* require LLM_MODEL and check it against the provider's model list ([#94](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/94)) ([fefcc0f](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/fefcc0f885c393bf15adf12961f1750c52df56c1))
* show the model, a read-only Settings dialog and an AI verification notice ([#95](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/95)) ([f56fc44](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/f56fc440d8a2d07cb550f3a554f03cfd9e70ab81))


### Bug Fixes

* JSON export carries the AI verification notice for model-written reports ([#101](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/101)) ([a8b3c1d](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/a8b3c1dd074c17631e93226f28593cf6b77c3700))
* report rejected cluster credentials instead of unreachable ([#106](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/106)) ([24c574c](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/24c574c06c4529b082cbdac9f5a1c3e690b54060))
* stop startup when the provider rejects the API key ([#105](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/105)) ([4a99352](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/4a9935289639c6e9da758399a417838783c46420))
* suggest LLM_REASONING_EFFORT=off when a model rejects the effort parameter ([#103](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/103)) ([ab636a4](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/ab636a47582486fb4dde66dbe2b59ab6810e21db))

## [1.5.0](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.4.0...v1.5.0) (2026-11-06)


### Features

* add Anthropic (Claude) as an LLM provider ([#84](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/84)) ([8c66361](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/8c663611a9a87edaac8ad3b5e23694041a853a09))
* make the model evaluation request timeout configurable ([#86](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/86)) ([2255677](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/2255677b4e779ce23a3c94baa33a163e6c8f4bac))

## [1.4.0](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.3.0...v1.4.0) (2026-11-06)


### Features

* support OpenAI reasoning models with LLM_REASONING_EFFORT ([#82](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/82)) ([1839efe](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/1839efe9797f1ee3162d961523be9f9b7e75d3d7))


### Bug Fixes

* state every validator check in the system prompt ([#83](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/83)) ([dcabb86](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/dcabb86e8717a810c747dc776c9346893b3b45d7))
* tell the model each safe command starts with kubectl ([#79](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/79)) ([9fbb422](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/9fbb422790c332cd146b54ccd4a52c318805d022))

## [1.3.0](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.2.0...v1.3.0) (2026-11-05)


### Features

* serve the guides at /docs ([#74](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/74)) ([f5ea083](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/f5ea08337ff24f654519624c4715a281b9b6c55e))


### Bug Fixes

* label the target name field Name for every kind ([#78](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/78)) ([9feb562](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/9feb5627845cfcbb40ae304b4d134c5c94e1ff89))

## [1.2.0](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.1.2...v1.2.0) (2026-11-05)


### Features

* read several clusters from one installation ([#68](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/68)) ([5b6d46b](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/5b6d46bebfa79af280b3051bf0792bbcf14ea4a3))

## [1.1.2](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.1.1...v1.1.2) (2026-11-05)


### Bug Fixes

* place hint bubbles before they show ([#60](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/60)) ([c4add53](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/c4add537c512ee57481e8f035662ece062e622f7))

## [1.1.1](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.1.0...v1.1.1) (2026-11-05)


### Bug Fixes

* place hint bubbles by the client size and wait for resizes in the test ([d059294](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/d059294cf94919ca712215753b4e0f490783b388))
* show hints in a bubble instead of expanding below ([2d2c7b2](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/2d2c7b2c4737526ffec920f8aea318de7c0a1527))

## [1.1.0](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/compare/v1.0.0...v1.1.0) (2026-11-05)


### Features

* delete incidents from the UI, one at a time or in bulk ([204c528](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/204c5287a7f9213cae8a20499cef255eb43a29bd))
* diagnose Ingresses, claims, autoscalers and nodes ([90d9bfd](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/90d9bfdd61ec1756cfa3e47886982b50f3302810))
* diagnose operator-configured custom resources ([1e2b60a](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/1e2b60ac253e38b39120992039a984d1eef2f1f3))
* diagnose StatefulSets, DaemonSets, Jobs and CronJobs ([36d6f69](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/36d6f6942da04cce3c7fd32c3b8d55c4f9bba532))
* explain confusing fields and report terms with "?" hints ([3b7156d](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/3b7156dbc68927afed09256d537fd03f13923513))
* join an existing database container's Docker network ([f70da3e](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/f70da3efc08f13daf7f1a51a31834b5d3a2f9427))
* lead with the conclusion and pick targets from the cluster ([010d339](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/010d339c47992c11473aa6c8f8c7240a2628d719))


### Bug Fixes

* act on the k3s pilot findings (results, UI, rules) ([338e7ea](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/338e7eaee578365952841170811a567ffb5b72d7))
* act on the k3s pilot findings for healthy results and empty data ([889a5ee](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/889a5eed5413a63a48d5fb645e2397787942f64f))
* ignore distribution-specific node conditions ([d0dff50](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/d0dff50b8f9eaad67f09dbb2e0ba9d13a631ebc6))
* keep Compose PostgreSQL on loopback and allow an existing database ([403d280](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/403d2802b959b9152dc80e65ae28ea1d5d606299))
* satisfy staticcheck in the resource kinds migration test ([1279211](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/1279211791abeefa84cd3339bfa05951c4edcea3))
* use logged errors and known names in rules reports ([925e328](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/925e328c997c65d3d221c824cc9561451ce13e73))
* wait for the server-side rollback in the purge cancellation test ([0d76e54](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/0d76e54eab4687d3c0c86ebb1bb2b0bc2745a553))

## 1.0.0 (2026-11-04)


### Features

* add bounded opt-in model evaluations ([ac08fcf](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/ac08fcfff335a0702c2f40a396de45040b21520e))
* add confirmed operator data deletion ([ec1cbf3](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/ec1cbf3d45ca61ebc577f08c0836ce0d8f93e368))
* add optional OIDC authentication ([#21](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/21)) ([512e1ca](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/512e1ca9ff9fa263154f70a08afebe7f90d87ac3))
* export attempts and mark older results in the UI ([d8a477b](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/d8a477b5a30447a8c634d068e6c179d25ea31ef1))
* expose build metadata in probes and UI ([0a88c45](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/0a88c4525d8919ac387071fded867d18175a16ba))
* expose investigation history and failed attempts ([06db1ed](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/06db1ed6668f6af45e20c69bbc55be1218935985))
* follow durable investigation jobs in the UI ([f05911c](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/f05911cb839028868e4bafb7e9c15086a9f48cc4))
* introduce evidence-linked assessment schema v2 ([73edb7d](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/73edb7de249d619fabb023f80b93b51694334dc7))
* persist investigation jobs with bounded recovery ([6cb1a66](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/6cb1a66e6d29f80546a24a264ec99d578bc61a13))
* run against a live cluster from Docker Compose ([c8ecea3](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/c8ecea311d69df5ef3b4c13e0023ef93fede6a9f))
* run investigations as durable PostgreSQL jobs ([c3d4f18](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/c3d4f184066cb8675cb6b5789480fa9c70c66cfa))
* show durable investigation progress in the UI ([6f54134](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/6f54134816813ec23c55a3a106275a8c61af31e5))
* show live or simulation mode from a runtime endpoint ([1b7f15b](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/1b7f15b452ed8d3e99908976ebe88a7a84e60c3c))
* version assessments with exact evidence citations ([b7b2422](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/b7b2422835995ecf0942d3bf12f2f7c3424c25d1))


### Bug Fixes

* bump Go to 1.27.3 for GO-2026-6809 ([#18](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/18)) ([f03fd35](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/f03fd35cc4aa646a90fa91b9eedb7dfde23cb57c))
* meet automated WCAG AA checks and keep narrow layouts intact ([92023c9](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/92023c917abb39e8da18ba6c40be3a88dc1a9e4a))
* patch called vulnerabilities in Go stdlib and x/net ([#15](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/15)) ([8cc8ef6](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/8cc8ef63298028b4bc36a6fc28491be2b7bdf6ca))
* redact data at persistence and LLM boundaries ([3edbd24](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/3edbd24f6497c397106c77d1c525b6ba1f0652c5))
* refuse a database schema newer than the binary ([ad30a22](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/ad30a225b9ed95f82386d861e6d71c13e9913e2c))
* resend rate-limited UI requests after Retry-After ([cfd45a6](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/cfd45a6b41e9d39b99f5f19005e9d1ca9986ebe7))
* resend rate-limited UI requests until the request timeout ([3c1274f](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/3c1274f0776a588b042a1cb2f8ff755704a984dc))
* serialize empty v2 lists as arrays ([384c09a](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/384c09a2675f60188abad912aea43fb9a6e9b09b))


### Continuous Integration

* release SemVer versions with release-please ([efeb710](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/efeb710968c66fe11aa03edf8f1a05eb5a1349b4))
