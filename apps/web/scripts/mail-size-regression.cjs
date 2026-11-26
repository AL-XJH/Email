const assert = require("node:assert/strict")
const fs = require("node:fs")
const { createRequire } = require("node:module")
const source = process.env.WEB_DIR || "/work/apps/web"
const projectRequire = createRequire(`${source}/package.json`)
const ts = projectRequire("typescript")
require.extensions[".ts"] = (module, filename) => {
  const { outputText } = ts.transpileModule(fs.readFileSync(filename, "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  })
  module._compile(outputText, filename)
}
const { MAX_TOTAL_ATTACHMENT_BYTES, MAX_ENCODED_MESSAGE_BYTES, estimatedMIMEBytes } = require(`${source}/src/lib/mail-size.ts`)
const mib = 1024 * 1024
assert.equal(MAX_TOTAL_ATTACHMENT_BYTES, 25 * mib)
assert.equal(MAX_ENCODED_MESSAGE_BYTES, 35 * mib)
assert.ok(estimatedMIMEBytes([{size:12*mib},{size:12*mib}],"attached","<p>attached</p>") < MAX_ENCODED_MESSAGE_BYTES)
assert.ok(estimatedMIMEBytes([{size:14*mib},{size:14*mib}],"attached","<p>attached</p>") > MAX_ENCODED_MESSAGE_BYTES)
assert.ok(estimatedMIMEBytes([{size:25*mib}],"x".repeat(2*mib),"<p>body</p>") > MAX_ENCODED_MESSAGE_BYTES)
assert.ok(estimatedMIMEBytes([],"中".repeat(5*mib),"中".repeat(5*mib)) > MAX_ENCODED_MESSAGE_BYTES)
assert.ok(estimatedMIMEBytes([],"中文","<p>中文</p>") > estimatedMIMEBytes([],"xx","<p>xx</p>"))
console.log("PASS MIME preflight: attachment/body encoding overhead, total cap and Unicode byte lengths")
