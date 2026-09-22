// JEVKIT_OPENCODE_PLUGIN — staged by `jevkit install opencode`. Do not edit in-place;
// change the embedded source under internal/agents/opencodeplugin/ and reinstall.
//
// Model-visible tool.execute.after output mutation is unproven on OpenCode
// (see ralph bundle/.opencode/plugins/SPIKE-output-mutation.md). Compaction
// relies on rewriting bash/shell commands to `jevkit exec -- ...` in
// tool.execute.before so the wrapper sees the real exit code.

const INSTALLED_BINARY = /*JEVKIT_BINARY*/"jevkit"/*JEVKIT_BINARY*/;

export type RunProcess = (
  argv: string[],
  options?: { stdin?: string },
) => Promise<{ stdout: string; exitCode: number }>;

export type BeforeInput = {
  tool?: string;
  sessionID?: string;
  callID?: string;
  args?: { command?: string };
};

export type BeforeOutput = {
  args?: Record<string, unknown> & { command?: string };
};

export type AfterInput = BeforeInput & {
  args?: { command?: string };
};

export type AfterOutput = {
  title?: string;
  output?: string;
  metadata?: unknown;
};

export function resolveJevkitBinary(
  env: NodeJS.ProcessEnv = process.env,
  installed: string = INSTALLED_BINARY,
): string {
  const fromEnv = (env.JEVKIT_BINARY || "").trim();
  if (fromEnv) return fromEnv;
  const trimmed = String(installed || "").trim();
  return trimmed || "jevkit";
}

export function isShellTool(tool: string | undefined | null): boolean {
  const normalized = String(tool || "")
    .trim()
    .toLowerCase();
  return (
    normalized === "bash" ||
    normalized === "shell" ||
    normalized === "command_execution"
  );
}

export async function defaultRunProcess(
  argv: string[],
  options: { stdin?: string } = {},
): Promise<{ stdout: string; exitCode: number }> {
  const bun = (globalThis as { Bun?: { spawn: Function } }).Bun;
  if (bun && typeof bun.spawn === "function") {
    const proc = bun.spawn(argv, {
      stdin: options.stdin != null ? new Blob([options.stdin]) : "ignore",
      stdout: "pipe",
      stderr: "pipe",
      env: process.env,
    });
    const [stdout, exitCode] = await Promise.all([
      new Response(proc.stdout).text(),
      proc.exited,
    ]);
    return { stdout, exitCode: Number(exitCode) };
  }
  const { spawn } = await import("node:child_process");
  return await new Promise((resolve, reject) => {
    const child = spawn(argv[0], argv.slice(1), {
      env: process.env,
      stdio: ["pipe", "pipe", "pipe"],
    });
    const chunks: Buffer[] = [];
    child.stdout.on("data", (chunk: Buffer) => chunks.push(chunk));
    child.stderr.resume();
    child.on("error", reject);
    child.on("close", (code) => {
      resolve({
        stdout: Buffer.concat(chunks).toString("utf8"),
        exitCode: code ?? 1,
      });
    });
    if (options.stdin != null) {
      child.stdin.end(options.stdin);
    } else {
      child.stdin.end();
    }
  });
}

function parseCommandResponse(stdout: string): string | null {
  const line = String(stdout || "")
    .trim()
    .split("\n")
    .pop();
  if (!line) return null;
  try {
    const parsed = JSON.parse(line) as { command?: unknown };
    if (typeof parsed.command === "string" && parsed.command.trim()) {
      return parsed.command;
    }
  } catch {
    /* fail open */
  }
  return null;
}

export async function handleToolExecuteBefore(
  input: BeforeInput,
  output: BeforeOutput,
  deps: { runProcess?: RunProcess; binary?: string; env?: NodeJS.ProcessEnv } = {},
): Promise<void> {
  if (!isShellTool(input.tool)) return;
  const args = output.args;
  if (args == null || typeof args !== "object") return;
  const command = typeof args.command === "string" ? args.command : "";
  if (!command.trim()) return;

  const binary = deps.binary || resolveJevkitBinary(deps.env);
  const run = deps.runProcess || defaultRunProcess;
  const payload = JSON.stringify({ input, output });
  let result: { stdout: string; exitCode: number };
  try {
    result = await run([binary, "hook", "opencode", "pre-tool"], {
      stdin: payload,
    });
  } catch {
    return;
  }
  if (result.exitCode !== 0) return;
  const rewritten = parseCommandResponse(result.stdout);
  if (rewritten) {
    args.command = rewritten;
  }
}

export async function handleToolExecuteAfter(
  input: AfterInput,
  output: AfterOutput,
  deps: { runProcess?: RunProcess; binary?: string; env?: NodeJS.ProcessEnv } = {},
): Promise<void> {
  // Telemetry only: shell out so Go records the hook invocation. Do not mutate
  // output.output — SPIKE leaves model-visible mutation unproven.
  const binary = deps.binary || resolveJevkitBinary(deps.env);
  const run = deps.runProcess || defaultRunProcess;
  const payload = JSON.stringify({ input, output });
  try {
    await run([binary, "hook", "opencode", "post-tool"], { stdin: payload });
  } catch {
    /* fail open */
  }
}

/** OpenCode auto-discovers exported async plugin factories from this file. */
export const JevkitRuntimeHooks = async (_ctx?: { directory?: string }) => {
  return {
    "tool.execute.before": async (input: BeforeInput, output: BeforeOutput) => {
      await handleToolExecuteBefore(input, output);
    },
    "tool.execute.after": async (input: AfterInput, output: AfterOutput) => {
      await handleToolExecuteAfter(input, output);
    },
  };
};
