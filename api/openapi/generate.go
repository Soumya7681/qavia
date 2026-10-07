// Package openapi holds the API contract and its generated code.
//
// qavia.yaml is written before any handler. gen/ is produced from it by
// `make gen` and is checked in; CI regenerates and fails on a diff, so an edited
// spec with stale generated code cannot merge. Never hand-edit gen/.
package openapi

// The generator is a tool dependency in go.mod, so its version is pinned with
// everything else and `go tool` needs no separate install step.
//
//go:generate go tool oapi-codegen -config cfg.yaml qavia.yaml

// The AI service publishes its own schema; ai.json is dumped from the FastAPI app
// by scripts/dump-ai-schema.sh and the client is generated from it. A Python
// response-model change that breaks the contract therefore breaks the Go build.
//
//go:generate go tool oapi-codegen -config ai-cfg.yaml ai.json
