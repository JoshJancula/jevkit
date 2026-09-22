import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  handleToolExecuteAfter,
  isShellTool,
  resolveJevkitBinary,
  type RunProcess,
} from "./jevkit-runtime-hooks.ts";

describe("resolveJevkitBinary", () => {
  it("prefers JEVKIT_BINARY env over installed default", () => {
    assert.equal(
      resolveJevkitBinary({ JEVKIT_BINARY: "/opt/jevkit" }, "jevkit"),
      "/opt/jevkit",
    );
  });

  it("falls back to installed binary", () => {
    assert.equal(resolveJevkitBinary({}, "/usr/local/bin/jevkit"), "/usr/local/bin/jevkit");
  });
});

describe("isShellTool", () => {
  it("matches bash/shell/command_execution", () => {
    assert.equal(isShellTool("bash"), true);
    assert.equal(isShellTool("Shell"), true);
    assert.equal(isShellTool("command_execution"), true);
    assert.equal(isShellTool("read"), false);
  });
});

describe("handleToolExecuteAfter", () => {
  it("shells out for telemetry and does not mutate output", async () => {
    const calls: { argv: string[]; stdin?: string }[] = [];
    const runProcess: RunProcess = async (argv, options) => {
      calls.push({ argv, stdin: options?.stdin });
      return {
        stdout: JSON.stringify({
          title: "mutated",
          output: "SHOULD_NOT_APPLY",
        }),
        exitCode: 0,
      };
    };
    const output = {
      title: "go test ./...",
      output: "ok  example/pkg\n",
      metadata: {},
    };
    await handleToolExecuteAfter(
      {
        tool: "bash",
        sessionID: "ses",
        callID: "call",
        args: { command: "go test ./..." },
      },
      output,
      { runProcess, binary: "jevkit" },
    );
    assert.equal(calls.length, 1);
    assert.deepEqual(calls[0].argv, ["jevkit", "_runtime", "dispatch", "--protocol", "1", "opencode", "post-tool"]);
    assert.equal(output.output, "ok  example/pkg\n");
    assert.equal(output.title, "go test ./...");
  });
});
