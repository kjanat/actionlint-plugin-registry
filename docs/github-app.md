# Registry GitHub App

The `actionlint-plugin-registry` GitHub App starts validation workflows and commits
accepted plugin records under its own bot identity. It is owned by `kjanat` and
needs access only to `kjanat/actionlint-registry`. Plugin publishers do not
install the App. Their CI jobs authorize submissions through OIDC.

## Permissions

The App requires the following repository permissions:

| Permission | Access         | Purpose                                                                 |
| ---------- | -------------- | ----------------------------------------------------------------------- |
| Actions    | Read and write | Start the registry validation workflow.                                 |
| Contents   | Read and write | Commit and push accepted version records.                               |
| Metadata   | Read-only      | Identify the repository. GitHub includes this permission automatically. |

The App does not need organization or account permissions. Webhooks, OAuth user
authorization, and device authorization are disabled. Install it on the registry
repository only.

The Worker requests an installation token with Actions write permission. The
publication job separately requests a token with Contents write permission. Both
tokens are restricted to the registry repository. The App's private keys can request
either permission, so keep them out of jobs that execute publisher plugins.

## Configure the deployment

The App registration is available in [the owner's GitHub settings](https://github.com/settings/apps/actionlint-plugin-registry).
Its App ID is `5371610`, its client ID is `Iv23lighv9xlr7gdPEKj`, and the registry
repository's installation ID is `172822417`. The installation ID is tracked in
`worker/wrangler.jsonc`; replacing the installation requires updating that value.
These identifiers are public configuration values, not credentials.

Generate separate private keys for the Worker and the publication workflow so
that either key can be revoked independently. The Worker accepts GitHub's downloaded
PKCS#1 PEM format as well as PKCS#8. Keep the keys out of Git, command output, and
workflow artifacts.

Configure these values in the registry repository:

| Setting                    | Kind             | Value                                                     |
| -------------------------- | ---------------- | --------------------------------------------------------- |
| `REGISTRY_APP_CLIENT_ID`   | Actions variable | Use the App's client ID.                                  |
| `REGISTRY_APP_PRIVATE_KEY` | Actions secret   | Use the private key created for the publication workflow. |

Configure these values for the Cloudflare Worker:

| Setting                      | Kind                      | Value                                                |
| ---------------------------- | ------------------------- | ---------------------------------------------------- |
| `GITHUB_APP_CLIENT_ID`       | Worker secret or variable | Use the same App client ID.                          |
| `GITHUB_APP_INSTALLATION_ID` | Worker secret or variable | Use the installation ID for the registry repository. |
| `GITHUB_APP_PRIVATE_KEY`     | Worker secret             | Use the private key created for the Worker.          |

The Worker also checks `REGISTRY_REPOSITORY_ID` against the expected registry
repository. Publisher requests cannot select another repository, workflow, branch,
or GitHub API endpoint.

The publication workflow fails with a configuration error if its App credentials
are missing. The Worker retains accepted requests when dispatch fails and retries
the notification. Scheduled runs can still claim those requests, but publication
requires valid App credentials.

## Verify the integration

After installing the App and configuring both sets of credentials, dispatch the
admission workflow through the Worker. Confirm that GitHub attributes the run to
the registry App and that the workflow can claim a request using its OIDC token.

For a complete publication test, submit an actual released plugin with valid
embedded metadata. Verify that the accepted record is committed by
`actionlint-plugin-registry[bot]`, that GitHub Pages publishes the record, and that
the request becomes `published` only after deployment succeeds. A successful
workflow dispatch alone does not verify publication.

The admission workflow invokes Pages explicitly. The standalone Pages workflow
skips push events from `actionlint-plugin-registry[bot]`, avoiding a second
deployment for the same App commit. Maintainer pushes still deploy, including
changes limited to registry records. Explicit calls from admission and manual
deployments remain enabled.

For GitHub's authentication details, see [installation access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app)
and [the official App-token action](https://github.com/actions/create-github-app-token).
