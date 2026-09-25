const test = require("node:test");
const assert = require("node:assert/strict");
const { resolveBinary, packages } = require("../lib/resolve");

test("maps supported platforms to their optional packages", () => {
  for (const [key, pkg] of Object.entries(packages)) {
    const [platform, arch] = key.split("-");
    const bin = resolveBinary(platform, arch, request => { assert.match(request, new RegExp(pkg.replace("/", "\\/"))); return `/x/${request}`; });
    assert.match(bin, /jevkit/);
  }
});
test("rejects unsupported and missing platforms", () => {
  assert.throws(() => resolveBinary("freebsd", "x64"), /unsupported platform/);
  assert.throws(() => resolveBinary("linux", "x64", () => { throw new Error("missing"); }), /missing optional package/);
});
