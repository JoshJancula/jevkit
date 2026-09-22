// Package jev is the typed client for TypeSafe AI's closed-set SystemOne API
// (https://api.typesafe.ai/v1/systemone). Jev is a classifier, not an LLM:
// callers send state text plus a set of typed questions and get back one
// typed answer per question.
//
// Failures carry an [Error] whose Code mirrors the ralph exit-code taxonomy:
// [CodeDeclined] (1), [CodeTransport] (2) and [CodeRejected] (3).
package jev
