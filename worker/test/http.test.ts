import { preflight } from "@/http.ts";
import { body } from "@/model.ts";
import { expect, test } from "bun:test";

const policy = {
  REGISTRY_ORIGIN: "https://plugins.actionlint.kjanat.dev",
  REGISTRY_REPOSITORY_ID: "1424600475",
  REGISTRY_WORKFLOW_REF:
    "kjanat/actionlint-plugin-registry/.github/workflows/admission.yml@refs/heads/master",
};

test("HTTP preflight denies unauthenticated writes before durable-object access", () => {
  for (
    const path of [
      "/v1/submissions",
      "/v1/claims",
      "/v1/claims/00000000-0000-4000-8000-000000000001/complete",
    ]
  ) {
    const response = preflight(new Request(`${policy.REGISTRY_ORIGIN}${path}`, { method: "POST" }), policy);
    expect(response?.status).toBe(401);
    expect(response?.headers.get("cache-control")).toBe("no-store");
  }
  expect(preflight(new Request(`${policy.REGISTRY_ORIGIN}/health`), policy)?.status).toBe(200);
  expect(
    preflight(new Request(`${policy.REGISTRY_ORIGIN}/health`), { ...policy, REGISTRY_REPOSITORY_ID: "" })
      ?.status,
  ).toBe(503);
  expect(preflight(new Request("https://evil.example/health"), policy)?.status).toBe(400);
  expect(preflight(new Request(`${policy.REGISTRY_ORIGIN}/v1/claims`), policy)?.status).toBe(404);
});

test("HTTP JSON decoding enforces content type, size and valid UTF-8", async () => {
  const url = `${policy.REGISTRY_ORIGIN}/v1/submissions`;
  await expect(body(new Request(url, { method: "POST", body: "{}" }))).rejects.toThrow(
    "application/json required",
  );
  await expect(
    body(
      new Request(url, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: " ".repeat(16385),
      }),
    ),
  ).rejects.toThrow("body too large");
  await expect(
    body(
      new Request(url, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: new Uint8Array([0xff]),
      }),
    ),
  ).rejects.toThrow("invalid JSON body");
});
