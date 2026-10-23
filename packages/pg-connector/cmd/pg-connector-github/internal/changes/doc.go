// Package changes binds the shared change engine (pkg/connectord/engine) to the
// GitHub daemon. It is a thin adapter:
//
//   - spec.go is the GitHub registration (Spec) and the one reference fold
//     (foldOwnerRepo, reached through Spec().FoldRef).
//   - classifier.go is the GitHub engine.Classifier over the pure rules of
//     classify and revalidate.
//   - facade.go keeps the package's exported API: New, Config, Engine (the
//     shared engine plus DetectSummary and the GitHub Membership), Lifecycle
//     and the aliases of the shared words and types.
//
// The detection logic itself (baselines, the settle barrier, appending,
// tombstones, query membership and the settle lifecycle) lives in the shared
// engine and is documented there. Its detection methods run INSIDE the store
// transaction that stores a refreshed summary or group (the seams of
// summary.Persister and groupsvc.Service), so a row and the content that caused
// it commit together or not at all; the engine holds no store handle.
package changes
