const path = require("node:path");

const packages = {
  "darwin-x64": "@jevkit/darwin-amd64",
  "darwin-arm64": "@jevkit/darwin-arm64",
  "linux-x64": "@jevkit/linux-amd64",
  "linux-arm64": "@jevkit/linux-arm64",
  "win32-x64": "@jevkit/win32-amd64",
  "win32-arm64": "@jevkit/win32-arm64"
};

function resolveBinary(platform, arch, resolveFn = require.resolve) {
  const name = packages[`${platform}-${arch}`];
  if (!name) throw new Error(`unsupported platform ${platform}/${arch}; download a release binary instead`);
  try { return resolveFn(path.join(name, "bin", platform === "win32" ? "jevkit.exe" : "jevkit")); }
  catch { throw new Error(`missing optional package ${name} for ${platform}/${arch}; reinstall jevkit with optional dependencies enabled`); }
}
module.exports = { resolveBinary, packages };
