import assert from "node:assert/strict";
import test from "node:test";

import {
  githubDestination,
  githubRegistration,
} from "../src/github-navigation.ts";

const state = "a".repeat(64);
const action = `https://github.com/settings/apps/new?state=${state}`;
const origin = "https://panel.example.test";
const manifest = {
  url: origin,
  redirect_url: `${origin}/github/manifest`,
  setup_url: `${origin}/github/installation`,
};

test("GitHub actions retain state and accept only the intended registration or installation path", () => {
  for (const [phase, path] of [
    ["manifest", "/settings/apps/new"],
    ["manifest", "/organizations/example-org/settings/apps/new"],
    ["installation", "/apps/example-app/installations/new"],
  ]) {
    const value = `https://github.com${path}?state=${state}`;
    assert.equal(githubDestination(value, phase), value, value);
  }
});

test("untrusted origins, credentials, paths, fragments and ambiguous state never become navigation actions", () => {
  for (const value of [
    `http://github.com/settings/apps/new?state=${state}`,
    `https://github.com.evil.example/settings/apps/new?state=${state}`,
    `https://evil.example/settings/apps/new?state=${state}`,
    `https://github.com:8443/settings/apps/new?state=${state}`,
    `https://operator@github.com/settings/apps/new?state=${state}`,
    `https://github.com/login?state=${state}`,
    `https://github.com/organizations/a%2fb/settings/apps/new?state=${state}`,
    `${action}#fragment`,
    `${action}&state=${state}`,
    `${action}&redirect=https://evil.example`,
    "https://github.com/settings/apps/new",
    "https://github.com/settings/apps/new?state=short",
    `https://github.com/settings/apps/new?state=${"z".repeat(64)}`,
    "javascript:alert(1)",
    "/settings/apps/new",
  ])
    assert.throws(() => githubDestination(value, "manifest"), undefined, value);
  assert.throws(() => githubDestination(action, "installation"));
  assert.throws(() =>
    githubDestination(
      `https://github.com/apps/example/installations/new?state=${state}`,
      "manifest",
    ),
  );
});

test("registration requires callbacks to the active HTTPS panel origin", () => {
  const json = JSON.stringify(manifest);
  assert.deepEqual(githubRegistration(action, json, origin), {
    action,
    manifestJson: json,
  });
  for (const field of ["url", "redirect_url", "setup_url"]) {
    const changed = JSON.stringify({
      ...manifest,
      [field]: "https://evil.example",
    });
    assert.throws(
      () => githubRegistration(action, changed, origin),
      undefined,
      field,
    );
  }
  for (const value of ["null", "[]", "invalid", "x".repeat(16_385)]) {
    assert.throws(
      () => githubRegistration(action, value, origin),
      undefined,
      value.slice(0, 20),
    );
  }
  assert.throws(() =>
    githubRegistration(action, json, "http://panel.example.test"),
  );
});
