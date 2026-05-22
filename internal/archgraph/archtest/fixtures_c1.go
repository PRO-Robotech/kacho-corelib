package archtest

// C1 fixtures (group E): a gRPC service (and sometimes a worker) plus L2
// notes under docs/arch, exercising entry-point / anchor completeness.

// SpecC1Pass — three entry-points (two RPCs + a worker), each anchored by
// exactly one L2 note. C1 passes 3/3. Scenario E1.
func SpecC1Pass() Spec {
	return Spec{
		Module: "example.com/c1e1",
		Services: []ServiceSpec{{
			FQN:     "kacho.cloud.vpc.v1.NetworkService",
			Methods: []string{"Create", "Delete"},
		}},
		Workers: []WorkerSpec{
			{Package: "worker", TypeName: "NetworkOutboxWorker", Method: "Run"},
		},
		Notes: []NoteSpec{{
			File: "network-lifecycle.md", Repo: "c1e1",
			RPCAnchors: []string{
				"kacho.cloud.vpc.v1.NetworkService/Create",
				"kacho.cloud.vpc.v1.NetworkService/Delete",
			},
			WorkerAnchors: []string{"NetworkOutboxWorker"},
			// FreshSHA so the fixture also passes C3 in an arch-audit
			// run; the CLI/C3 tests resolve it to a genuine hash. C1's
			// own unit test ignores source_sha entirely.
			SourceSHA: FreshSHA,
		}},
	}
}

// SpecC1Undocumented — NetworkService exposes three RPCs but the note
// anchors only two; Update is undocumented. C1 FAILs. Scenario E2.
func SpecC1Undocumented() Spec {
	return Spec{
		Module: "example.com/c1e2",
		Services: []ServiceSpec{{
			FQN:     "kacho.cloud.vpc.v1.NetworkService",
			Methods: []string{"Create", "Update", "Delete"},
		}},
		Notes: []NoteSpec{{
			File: "network-lifecycle.md", Repo: "c1e2",
			RPCAnchors: []string{
				"kacho.cloud.vpc.v1.NetworkService/Create",
				"kacho.cloud.vpc.v1.NetworkService/Delete",
			},
		}},
	}
}

// SpecC1StaleAnchor — the note anchors a Patch RPC absent from the code.
// C1 FAILs with a stale-anchor finding. Scenario E3.
func SpecC1StaleAnchor() Spec {
	return Spec{
		Module: "example.com/c1e3",
		Services: []ServiceSpec{{
			FQN:     "kacho.cloud.vpc.v1.NetworkService",
			Methods: []string{"Create", "Delete"},
		}},
		Notes: []NoteSpec{{
			File: "network-lifecycle.md", Repo: "c1e3",
			RPCAnchors: []string{
				"kacho.cloud.vpc.v1.NetworkService/Create",
				"kacho.cloud.vpc.v1.NetworkService/Delete",
				"kacho.cloud.vpc.v1.NetworkService/Patch",
			},
		}},
	}
}

// SpecC1DuplicateAnchor — two notes both anchor Create. C1 FAILs with a
// duplicate finding. Scenario E4.
func SpecC1DuplicateAnchor() Spec {
	return Spec{
		Module: "example.com/c1e4",
		Services: []ServiceSpec{{
			FQN:     "kacho.cloud.vpc.v1.NetworkService",
			Methods: []string{"Create", "Delete"},
		}},
		Notes: []NoteSpec{
			{
				File: "network-bootstrap.md", Repo: "c1e4",
				RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
			},
			{
				File: "network-lifecycle.md", Repo: "c1e4",
				RPCAnchors: []string{
					"kacho.cloud.vpc.v1.NetworkService/Create",
					"kacho.cloud.vpc.v1.NetworkService/Delete",
				},
			},
		},
	}
}

// SpecC1NonConventionalWorker — a worker started without the convention
// is not an entry-point, so an L2 anchor for it reads as stale; the
// inventory carries an unrecognised-worker hint. Scenario E5.
func SpecC1NonConventionalWorker() Spec {
	return Spec{
		Module: "example.com/c1e5",
		NoMain: true,
		Notes: []NoteSpec{{
			File: "network-outbox.md", Repo: "c1e5",
			WorkerAnchors: []string{"NetworkOutboxWorker"},
		}},
		Files: map[string]string{
			"cmd/svc/main.go": nonConventionalMain("c1e5"),
		},
	}
}

// SpecC1EmptyAnchors — one note covers both RPCs, a second note has an
// empty anchors list (planned). The empty note must be ignored. C1
// passes 2/2. Scenario E6.
func SpecC1EmptyAnchors() Spec {
	return Spec{
		Module: "example.com/c1e6",
		Services: []ServiceSpec{{
			FQN:     "kacho.cloud.vpc.v1.NetworkService",
			Methods: []string{"Create", "Delete"},
		}},
		Notes: []NoteSpec{
			{
				File: "network-lifecycle.md", Repo: "c1e6",
				RPCAnchors: []string{
					"kacho.cloud.vpc.v1.NetworkService/Create",
					"kacho.cloud.vpc.v1.NetworkService/Delete",
				},
				// FreshSHA so the fixture also passes C3 under arch-audit.
				SourceSHA: FreshSHA,
			},
			{
				File: "network-planned.md", Repo: "c1e6",
				Status: "planned", SourceSHA: "",
			},
		},
	}
}
