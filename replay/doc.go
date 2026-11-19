// Package replay hosts the chain-agnostic replay acceptance harness: timing
// collection, the report format, the verdicts, the stall watchdog, background
// service wiring, and config bootstrapping. Carrying the acceptance build
// tag, it is imported only by test files; replay/evm owns the EVM mock node,
// fixture handling, and the per-chain specs and test entry points. Chain
// identity reaches the harness only as parameters, so a future non-EVM
// replay suite can reuse it unchanged.
//
// This file exists for the build tag, not for code: without it the directory
// is empty of untagged files, explicit-path tooling (go list, go vet, go
// build, lint, dead-code gates) reports "build constraints exclude all Go
// files", and the package cannot be resolved. The placeholder keeps the
// package loadable in untagged runs and compiles to no symbols.
package replay
