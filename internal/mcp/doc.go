// Package mcp is the jevkit decision server: a stdio JSON-RPC MCP server
// (built on the official Go SDK) that exposes Jev as five tools.
//
// The curated tools (jev_classify_request, jev_classify_failure,
// jev_rank_relevance) each fix one registered, versioned question set and
// return the typed answer plus the registry's act/gather/fallback decision.
// jev_developer_assess dispatches to a small, versioned, opt-in set of
// developer.* question sets built for coding-agent decision support.
// jev_ask is the raw, unversioned escape hatch. Every tool redacts state
// before it reaches the transport.
//
// Agents decide whether to call these tools from very little: the server's
// initialize [Instructions] and the tool descriptions. Registry-backed results
// therefore carry one line of guidance next to the decision, so a gather says
// what evidence to add instead of reading as no answer.
//
// stdout is the protocol channel; all logging goes to the configured log
// writer (stderr in production). The API key is resolved by the injected
// client, never by a caller, and is never logged or returned.
package mcp
