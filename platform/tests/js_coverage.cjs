const fs = require("node:fs");
const path = require("node:path");
const { createRequire } = require("node:module");

const [mode, directory] = process.argv.slice(2);
const dependencyDirectory = process.env.ACCEPTANCE_JS_DEPS ||
  path.resolve(__dirname, "../test-results/javascript");
const load = createRequire(path.resolve(dependencyDirectory, "package.json"));
const source = path.resolve(__dirname, "../../gateway/web/app.js");

if (mode === "instrument") {
  const { createInstrumenter } = load("istanbul-lib-instrument");
  const instrumenter = createInstrumenter({ esModules: false, compact: false });
  const code = instrumenter.instrumentSync(fs.readFileSync(source, "utf8"), source);
  fs.writeFileSync(path.join(directory, "app.instrumented.js"), code);
} else if (mode === "report") {
  const { createCoverageMap } = load("istanbul-lib-coverage");
  const coverage = createCoverageMap({});
  const raw = path.join(directory, "js-raw");
  for (const file of fs.readdirSync(raw).sort()) {
    const capture = JSON.parse(fs.readFileSync(path.join(raw, file), "utf8"));
    if (Object.keys(capture).some((file) => file !== source)) {
      throw new Error("Unexpected asset in JavaScript coverage");
    }
    coverage.merge(capture);
  }
  const summary = coverage.fileCoverageFor(source).toSummary();
  fs.writeFileSync(path.join(directory, "javascript-coverage.json"), JSON.stringify(coverage.toJSON(), null, 2));
  process.stdout.write(JSON.stringify(summary.statements));
} else {
  throw new Error("Specify mode instrument or report");
}
