// Package compact is the deterministic shell-output compactor behind the jev
// tier's fallback path. It is a port of the pieces of ralph's
// shell-output-compact.py that the fallback needs: command classification,
// hard passthrough for source-output families, collapse of repeated line runs
// and byte-budget assembly. Family compactors that summarize (git status,
// test runners, installers, ...) are not ported; output of those commands is
// left unchanged.
package compact
