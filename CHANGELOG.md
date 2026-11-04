# Changelog

## 1.0.0 (2026-11-04)


### Features

* add bounded opt-in model evaluations ([ac08fcf](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/ac08fcfff335a0702c2f40a396de45040b21520e))
* add bounded opt-in model evaluations ([4045e04](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/4045e04cfea166f30c90d77be776c4027310fe85))
* add confirmed operator data deletion ([ec1cbf3](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/ec1cbf3d45ca61ebc577f08c0836ce0d8f93e368))
* add confirmed operator data deletion ([4970f17](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/4970f17bdb5e9fb9cdb645c5adedb2836fffe313))
* add optional OIDC authentication ([#21](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/21)) ([512e1ca](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/512e1ca9ff9fa263154f70a08afebe7f90d87ac3))
* export attempts and mark older results in the UI ([d8a477b](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/d8a477b5a30447a8c634d068e6c179d25ea31ef1))
* export attempts and mark older results in the UI ([fbea1f9](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/fbea1f9921b9f1cd41f60306b190a33b9dd2a103))
* expose build metadata in probes and UI ([0a88c45](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/0a88c4525d8919ac387071fded867d18175a16ba))
* expose build metadata in probes and UI ([77eaf71](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/77eaf715426c58fc50d278c1f9250c0202fd1690))
* expose investigation history and failed attempts ([06db1ed](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/06db1ed6668f6af45e20c69bbc55be1218935985))
* expose investigation history and failed attempts ([1caebf3](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/1caebf32678864dff0c4bea59c22cc28be5bc783))
* follow durable investigation jobs in the UI ([f05911c](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/f05911cb839028868e4bafb7e9c15086a9f48cc4))
* introduce evidence-linked assessment schema v2 ([73edb7d](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/73edb7de249d619fabb023f80b93b51694334dc7))
* persist investigation jobs with bounded recovery ([6cb1a66](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/6cb1a66e6d29f80546a24a264ec99d578bc61a13))
* run against a live cluster from Docker Compose ([c8ecea3](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/c8ecea311d69df5ef3b4c13e0023ef93fede6a9f))
* run against a live cluster from Docker Compose ([5894cb3](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/5894cb35abcf223bd8a63bfd1ae7383c797e2196))
* run investigations as durable PostgreSQL jobs ([c3d4f18](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/c3d4f184066cb8675cb6b5789480fa9c70c66cfa))
* show durable investigation progress in the UI ([6f54134](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/6f54134816813ec23c55a3a106275a8c61af31e5))
* show live or simulation mode from a runtime endpoint ([1b7f15b](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/1b7f15b452ed8d3e99908976ebe88a7a84e60c3c))
* show live or simulation mode from a runtime endpoint ([7f66c37](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/7f66c379deac6100f1fd97915ab7457173a93971))
* version assessments with exact evidence citations ([b7b2422](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/b7b2422835995ecf0942d3bf12f2f7c3424c25d1))


### Bug Fixes

* bump Go to 1.27.3 for GO-2026-6809 ([#18](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/18)) ([f03fd35](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/f03fd35cc4aa646a90fa91b9eedb7dfde23cb57c))
* meet automated WCAG AA checks and keep narrow layouts intact ([92023c9](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/92023c917abb39e8da18ba6c40be3a88dc1a9e4a))
* meet automated WCAG AA checks and keep narrow layouts intact ([aa38ab5](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/aa38ab5bf6de86e791f1864848c19feb523c06ba))
* patch called vulnerabilities in Go stdlib and x/net ([#15](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/issues/15)) ([8cc8ef6](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/8cc8ef63298028b4bc36a6fc28491be2b7bdf6ca))
* redact data at persistence and LLM boundaries ([3edbd24](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/3edbd24f6497c397106c77d1c525b6ba1f0652c5))
* redact data at persistence and LLM boundaries ([1984d09](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/1984d091d8b790e57d09a2e617928a70789902cb))
* refuse a database schema newer than the binary ([ad30a22](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/ad30a225b9ed95f82386d861e6d71c13e9913e2c))
* refuse a database schema newer than the binary ([4765b1b](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/4765b1b7d1acaf31fe38b5c93d6ec13959b0b85d))
* resend rate-limited UI requests after Retry-After ([cfd45a6](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/cfd45a6b41e9d39b99f5f19005e9d1ca9986ebe7))
* resend rate-limited UI requests after Retry-After ([e2f1d45](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/e2f1d45f738fd97e1f2a75dd72b722658b581398))
* resend rate-limited UI requests until the request timeout ([3c1274f](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/3c1274f0776a588b042a1cb2f8ff755704a984dc))
* resend rate-limited UI requests until the request timeout ([2b62e17](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/2b62e178297dacba6d0cfbb29edd268acc514f36))
* serialize empty v2 lists as arrays ([384c09a](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/384c09a2675f60188abad912aea43fb9a6e9b09b))


### Continuous Integration

* release SemVer versions with release-please ([efeb710](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/commit/efeb710968c66fe11aa03edf8f1a05eb5a1349b4))
