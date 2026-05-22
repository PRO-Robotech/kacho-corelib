---
level: functionality          # L2: a unit of functionality
repo: kacho-vpc
# anchors are the entry-points of this functionality
anchors:
  - rpc: kacho.cloud.vpc.v1.NetworkService/Create
  - rpc: kacho.cloud.vpc.v1.NetworkService/Delete   # delete path
  - worker: NetworkOutboxWorker
status: implemented           # computed by archgraph
source_sha: a1b2c3d
---
# Network lifecycle

This note describes how a VPC Network is created and deleted.

The body must survive a write-back untouched.
