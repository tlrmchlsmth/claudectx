# Design: `claudectx inject` — profiles in containers

## Problem

A profile is only useful where the tool runs. Increasingly that is inside a
container — a local docker/podman container, or a pod in a Kubernetes
cluster — with none of the host's config or credentials. Getting a working
`claude`/`codex` in there today means hand-copying files and hand-translating
the macOS Keychain credential into the file form Linux expects.

## Shape

One command, no intermediate artifacts:

```sh
claudectx inject claude pod/vllm-decode-0            # current profile → k8s pod
claudectx inject claude work pod/foo -n dev -c main  # named profile, ns, container
claudectx inject codex docker:a1b2c3                 # docker/podman container
claudectx inject claude work dir:./cfg               # escape hatch: materialize locally
```

An earlier draft split this into `export` (make a snapshot) + `inject`
(deliver it). Folded into one verb deliberately: nobody wants the tarball,
and with no export command there is no credentials-bearing file at rest
waiting to be committed or baked into an image. Secrets exist only in flight,
inside the exec stream to a specific target. The snapshot logic lives in
`internal/snapshot` with `inject` as its only consumer.

## What gets injected

A *snapshot* of the profile: a Linux-ready config dir.

- **Config** — settings, skills, agents, commands, `CLAUDE.md`/`AGENTS.md`,
  hooks, plugins, `config.toml`. Copied as-is.
- **Host-private noise — excluded by default.** Transcript history
  (`projects/`), `todos/`, shell snapshots, caches, logs, IDE/session state,
  the bundled `local/` install. They are most of the bytes and none of the
  point, and history is private.
- **`.claude.json`** — copied with the `projects` key stripped (per-host
  paths and history that mean nothing in the container); `mcpServers`,
  onboarding flags, and everything else survive. When injecting the current
  profile the live `~/.claude.json` is read (the profile copy is only
  captured on switch-away).
- **Credentials — opt-in (`--with-creds`), translated, stripped.** See below.

The snapshot is a one-way fork: nothing syncs back. Logins or history
written inside the container stay there.

### Destination

Defaults to the tool's own default config dir in the target —
`$HOME/.claude` / `$HOME/.codex` — so no env var is needed inside the
container; the tool just works. `--dest` overrides (then you set
`CLAUDE_CONFIG_DIR`/`CODEX_HOME` yourself; the command prints the hint).

## Credentials

Threat model: anyone with `pods/exec` (or docker exec) on the target reads
anything the pod can read. No injection scheme hides a working credential
from them, so the policy is to minimize the value of what can be stolen.

- **Claude OAuth: access token only, never the refresh token (default).**
  The keychain stash / `.credentials.json` holds a short-lived
  `accessToken` and a long-lived `refreshToken`. `inject` ships only the
  access token: claude in the container works until `expiresAt`, then
  politely asks for login (verified against Claude Code 2.1.x — clean
  one-line 401, exit 1, no crash, with or without an expired/absent refresh
  token). Renewal = re-run inject; the refresh token never leaves the host
  keychain. A stolen copy dies on its own in hours. `--with-refresh-token`
  opts out for long-lived personal containers. MCP OAuth entries get the
  same refresh-token strip.
- **Credential source order (claude):** live Keychain when injecting the
  current profile on macOS (the stash only exists for inactive profiles),
  else the profile's keychain stash, else `home/.credentials.json` (Linux).
  Whatever the source, the container always receives `.credentials.json`
  (mode 0600) — the form Linux Claude Code reads.
- **Codex:** `auth.json` travels verbatim under `--with-creds` (an API key
  written by `codex login --with-api-key` is already the right shape;
  claudectx does not attempt to dissect ChatGPT logins).
- **Vertex / API-key profiles need no credential step at all** — their auth
  is `settings.json` env config and travels with the config. For durable
  in-cluster use these (or GKE Workload Identity, which puts no secret
  material in the pod at all) are the recommended modes; OAuth snapshots
  are for ephemeral, interactive use.
- Secrets are never passed via argv and never written to local disk
  (except an explicit `dir:` target, which warns).

## Transport

`tar` streamed over the runtime's exec channel — the lowest common
denominator that needs no agent in the container:

```
kubectl exec -i [-n NS] [-c CTR] POD -- sh -c 'mkdir -p ... && tar -xf - -C ...'
docker  exec -i CTR sh -c '...'
```

claudectx shells out to `kubectl`/`docker`/`podman` (inheriting kubeconfig,
contexts, auth plugins) rather than linking client libraries. Requires
`sh` and `tar` in the target image — the same constraint as `kubectl cp`.
Distroless images fail with a clear error.

`inject` is idempotent: re-running refreshes whatever is there. Re-inject to
renew an expired access token, or after changing your profile.

## `claudectx exec` — sessions for shared pods

For pods other people can exec into, even an access-token file is more
exposure than necessary. `exec` is the session-scoped sibling of inject:

```sh
claudectx exec claude work pod/foo -n dev          # config synced, then: claude
claudectx exec claude pod/foo -- bash              # custom command
```

It syncs the *config* snapshot (never credentials), then opens an exec
session with the credential held only in that session's environment —
`CLAUDE_CODE_OAUTH_TOKEN` (the access token) or `OPENAI_API_KEY`. Nothing
credential-shaped is left on the container filesystem: when your session
ends, there is nothing to find.

Delivery mechanics: the token is piped over stdin into a 0600 file on
`/dev/shm` (RAM-backed; `/tmp` fallback), which the session script reads
into the env and removes before exec'ing your command — it exists for the
instant between the two execs and never appears on any argv, host or
container. If the session fails to start, a best-effort cleanup exec
removes the handoff file. Residual exposure: someone exec'd in *while your
session runs* can read your process's environ (same-UID containers); gone
when you leave.

A session command's own exit status passes through claudectx silently, so
`claudectx exec ... -- claude -p "..."` is scriptable.

Profiles with no env-deliverable credential (Vertex, API-key-in-settings,
Codex ChatGPT logins) run config-only sessions — for Vertex/settings
profiles that is already a fully working session.

## Non-goals (now)

- **K8s manifest generation** (Secret + initContainer): adds `secrets get`
  RBAC and etcd as extra credential readers — strictly worse under the
  exec threat model. Reconsider if declarative workflows demand it.
- **Two-way sync** of container-side state back to the profile.
- **Auto-refresh daemons.** Re-running inject is the refresh.
