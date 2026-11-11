import assert from "node:assert/strict";
import test from "node:test";

import { secretChangeProblem } from "../src/secret-changes.ts";

test("secret changes preserve omissions, permit empty and whitespace values, and atomically replace names within capacity", () => {
  assert.equal(
    secretChangeProblem(
      [
        { name: "EMPTY", value: "" },
        { name: "SPACES", value: "  " },
      ],
      [],
      ["KEEP"],
    ),
    undefined,
  );
  const names = Array.from({ length: 64 }, (_, index) => `OLD_${index}`);
  const upserts = names.map((_, index) => ({
    name: `NEW_${index}`,
    value: "",
  }));
  assert.equal(secretChangeProblem(upserts, names, names), undefined);
  assert.match(
    secretChangeProblem([{ name: "EXTRA", value: "" }], [], names),
    /at most 64/,
  );
});

test("duplicate secret names and overlapping upserts and removals are rejected without including values in errors", () => {
  const value = "synthetic-fixture-value";
  for (const [upserts, removals] of [
    [
      [
        { name: "TOKEN", value },
        { name: "TOKEN", value },
      ],
      [],
    ],
    [[{ name: "TOKEN", value }], ["TOKEN"]],
    [[], ["TOKEN", "TOKEN"]],
    [[{ name: "bad-name", value }], []],
    [[], ["bad-name"]],
    [[], []],
  ]) {
    const problem = secretChangeProblem(upserts, removals, []);
    assert.equal(typeof problem, "string");
    assert.equal(problem.includes(value), false);
  }
});

test("secret value limits count UTF-8 bytes, and request/name bounds reject oversized changes", () => {
  assert.equal(
    secretChangeProblem([{ name: "TOKEN", value: "é".repeat(8192) }], [], []),
    undefined,
  );
  assert.match(
    secretChangeProblem([{ name: "TOKEN", value: "é".repeat(8193) }], [], []),
    /16384 bytes/,
  );
  assert.equal(
    secretChangeProblem([{ name: `_${"A".repeat(127)}`, value: "" }], [], []),
    undefined,
  );
  assert.match(
    secretChangeProblem([{ name: `_${"A".repeat(128)}`, value: "" }], [], []),
    /128 ASCII/,
  );
  assert.match(
    secretChangeProblem(
      Array.from({ length: 65 }, (_, index) => ({
        name: `ITEM_${index}`,
        value: "",
      })),
      [],
      [],
    ),
    /at most 64/,
  );
  assert.match(
    secretChangeProblem(
      [],
      Array.from({ length: 65 }, (_, index) => `ITEM_${index}`),
      [],
    ),
    /at most 64/,
  );
});
