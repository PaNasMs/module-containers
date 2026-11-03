import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import ts from "typescript";

// Use the project's compiler so the same tests run on all supported Node versions.
const js = ts
  .transpileModule(
    readFileSync(
      new URL("../frontend/compose-model.ts", import.meta.url),
      "utf8",
    ),
    {
      compilerOptions: {
        target: ts.ScriptTarget.ES2022,
        module: ts.ModuleKind.ESNext,
      },
    },
  )
  .outputText.replace(
    /from ["']yaml["']/,
    "from " + JSON.stringify(import.meta.resolve("yaml")),
  );
const { patchCompose, parseCompose } = await import(
  "data:text/javascript;base64," + Buffer.from(js).toString("base64")
);
const original =
  "# stack\nx-common: &common\n  image: 'nginx:${TAG}'\nservices:\n  web:\n    <<: *common\n    ports: [\"8080:80\", '443:443'] # keep\n    healthcheck:\n      test: [CMD, true]\n";
let draft = patchCompose(original, ["services", "web", "ports", 0], "8081:80");
assert.equal(draft, original.replace("8080:80", "8081:80"));
draft = patchCompose(draft, ["services", "web", "image"], "redis:7");
assert.ok(
  draft.includes("image: 'nginx:${TAG}'") && draft.includes("<<: *common"),
);
draft = patchCompose(draft, ["services", "web", "ports", 1], undefined);
draft = patchCompose(draft, ["services", "worker"], { image: "alpine" });
draft = patchCompose(draft, ["networks", "shared"], {});
assert.deepEqual(parseCompose(draft).model.services.web.ports, ["8081:80"]);
assert.equal(parseCompose(draft).model.services.worker.image, "alpine");
assert.ok(draft.includes("test: [CMD, true]") && draft.includes("# keep"));
assert.throws(() => patchCompose(original, ["x-common", "image"], "changed"));
assert.throws(() => parseCompose("services: ["));
