// projectLabel: a project prints by its last path segment; filters keep
// the raw value.

import { test } from "node:test";
import assert from "node:assert/strict";
import { projectLabel } from "./filters.ts";

test("projectLabel prints the last path segment", () => {
  assert.equal(projectLabel(""), "(none)");
  assert.equal(projectLabel("/a/b/c"), "c");
  assert.equal(projectLabel("~/Projects/tatitok"), "tatitok");
});

test("projectLabel leaves a value without a slash unchanged", () => {
  assert.equal(projectLabel("-home-x-y"), "-home-x-y");
  assert.equal(projectLabel("~"), "~");
});

test("projectLabel ignores trailing slashes; an all-slash value prints as is", () => {
  assert.equal(projectLabel("/a/b/"), "b");
  assert.equal(projectLabel("/a/b//"), "b");
  assert.equal(projectLabel("/"), "/");
});
