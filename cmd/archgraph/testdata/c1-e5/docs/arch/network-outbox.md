---
level: functionality
repo: c1e5
anchors:
  - worker: NetworkOutboxWorker
status: implemented
source_sha: a1b2c3d
---
# Network outbox

Anchors the NetworkOutboxWorker, which is started without the recognised
New<Name> + Run/Start convention — C1 sees a stale anchor and emits a
clarifying hint about the convention.
