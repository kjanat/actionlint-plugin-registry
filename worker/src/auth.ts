import { type Authorization, HttpError, type Identity, type Submission, text } from "@/model.ts";
import { createRemoteJWKSet, decodeJwt, type JWTPayload, jwtVerify, type JWTVerifyGetKey } from "jose";

export const githubIssuer = "https://token.actions.githubusercontent.com";
export const gitlabIssuer = "https://gitlab.com";
const keys = new Map([
  [
    githubIssuer,
    createRemoteJWKSet(new URL("https://token.actions.githubusercontent.com/.well-known/jwks"), {
      timeoutDuration: 5000,
    }),
  ],
  [
    gitlabIssuer,
    createRemoteJWKSet(new URL("https://gitlab.com/oauth/discovery/keys"), { timeoutDuration: 5000 }),
  ],
]);

export interface Policy {
  REGISTRY_ORIGIN: string;
  REGISTRY_REPOSITORY_ID: string;
  REGISTRY_WORKFLOW_REF: string;
}

export async function verify(
  token: string,
  audience: string,
  testKeys?: ReadonlyMap<string, JWTVerifyGetKey>,
): Promise<JWTPayload> {
  try {
    if (token.length > 16384) throw new Error("oversize token");
    const issuer = decodeJwt(token).iss;
    const key = typeof issuer === "string" ? (testKeys ?? keys).get(issuer) : undefined;
    if (!key || (issuer !== githubIssuer && issuer !== gitlabIssuer)) throw new Error("issuer denied");
    const { payload } = await jwtVerify(token, key, {
      issuer,
      audience,
      algorithms: ["RS256"],
      requiredClaims: ["iss", "sub", "aud", "iat", "nbf", "exp", "jti"],
      maxTokenAge: 600,
      clockTolerance: 5,
    });
    if (payload.aud !== audience) throw new Error("exact audience required");
    if (typeof payload.iat !== "number" || typeof payload.exp !== "number" || payload.exp <= payload.iat) {
      throw new Error("invalid lifetime");
    }
    return payload;
  } catch {
    throw new HttpError(401, "invalid OIDC token");
  }
}

function id(value: unknown): string {
  const result = text(value, 32);
  if (!/^[1-9][0-9]*$/u.test(result)) throw new HttpError(403, "invalid identity ID");
  return result;
}

function commit(value: unknown): string {
  const result = text(value, 40);
  if (!/^[0-9a-f]{40}$/u.test(result)) throw new HttpError(403, "invalid commit");
  return result;
}

function githubIdentity(p: JWTPayload): Identity {
  if (p.iss !== githubIssuer || p.repository_visibility !== "public") {
    throw new HttpError(403, "public GitHub repository required");
  }
  return {
    issuer: githubIssuer,
    subject: text(p.sub),
    repository_id: id(p.repository_id),
    owner_id: id(p.repository_owner_id),
    repository: text(p.repository, 256).toLowerCase(),
    ref: text(p.ref),
    commit: commit(p.sha),
    workflow: text(p.workflow_ref),
    run_id: `${id(p.run_id)}:${id(p.run_attempt)}`,
  };
}

export function publisher(p: JWTPayload, s: Submission): Authorization {
  let identity: Identity;
  if (s.provider === "github") {
    identity = githubIdentity(p);
    if (!["push", "release", "workflow_dispatch"].includes(text(p.event_name)) || p.ref_type !== "tag") {
      throw new HttpError(403, "tag workflow required");
    }
    if (
      !identity.workflow.toLowerCase().startsWith(`${identity.repository}/.github/workflows/`)
      || !identity.workflow.endsWith(`@refs/tags/${s.tag}`)
    ) throw new HttpError(403, "own tag workflow required");
  } else {
    if (
      p.iss !== gitlabIssuer || p.project_visibility !== "public" || p.ref_type !== "tag"
      || !["push", "web"].includes(text(p.pipeline_source))
    ) throw new HttpError(403, "public GitLab tag pipeline required");
    const repository = text(p.job_project_path, 256).toLowerCase();
    const repositoryID = id(p.job_project_id);
    if (
      id(p.project_id) !== repositoryID || text(p.project_path, 256).toLowerCase() !== repository
      || p.ref !== s.tag || p.ref_path !== `refs/tags/${s.tag}`
    ) throw new HttpError(403, "own project tag required");
    const subject = text(p.sub);
    identity = {
      issuer: gitlabIssuer,
      subject,
      repository_id: repositoryID,
      owner_id: id(p.job_namespace_id),
      repository,
      ref: text(p.ref_path),
      commit: commit(p.sha),
      workflow: text(p.ci_config_ref_uri),
      run_id: `${id(p.pipeline_id)}:${id(p.job_id)}`,
    };
    if (
      !identity.workflow.toLowerCase().startsWith(`gitlab.com/${repository}//`)
      || !identity.workflow.endsWith(`@refs/tags/${s.tag}`) || commit(p.ci_config_sha) !== identity.commit
    ) throw new HttpError(403, "own tag CI configuration required");
  }
  if (identity.repository !== s.repository || identity.ref !== `refs/tags/${s.tag}`) {
    throw new HttpError(403, "submission does not match publisher");
  }
  if (typeof p.exp !== "number" || typeof p.iat !== "number") throw new HttpError(403, "timestamps required");
  return { identity, jti: text(p.jti, 256), expires: Math.min(p.exp, p.iat + 605) };
}

export function consumer(p: JWTPayload, policy: Policy): Identity {
  const identity = githubIdentity(p);
  if (
    identity.repository_id !== policy.REGISTRY_REPOSITORY_ID
    || identity.workflow !== policy.REGISTRY_WORKFLOW_REF || identity.ref !== "refs/heads/master"
    || p.ref_type !== "branch" || !["schedule", "workflow_dispatch"].includes(text(p.event_name))
  ) throw new HttpError(403, "registry validator workflow required");
  return identity;
}

export function bearer(request: Request): string {
  const header = request.headers.get("authorization");
  if (!header?.startsWith("Bearer ") || header.length > 16400) {
    throw new HttpError(401, "Bearer OIDC token required");
  }
  return header.slice(7);
}
