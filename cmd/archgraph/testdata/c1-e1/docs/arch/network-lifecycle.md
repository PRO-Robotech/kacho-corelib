---
level: functionality
repo: c1e1
anchors:
  - rpc: kacho.cloud.vpc.v1.NetworkService/Create
  - rpc: kacho.cloud.vpc.v1.NetworkService/Delete
  - worker: NetworkOutboxWorker
status: implemented
source_sha: a1b2c3d
---
# Network lifecycle

This L2 note anchors every entry-point of the c1e1 fixture: the two
NetworkService RPCs and the outbox worker.
