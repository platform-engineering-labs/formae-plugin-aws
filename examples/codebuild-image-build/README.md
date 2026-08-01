# Build a container image with CodeBuild, with everything declared

[`main.pkl`](main.pkl) builds a container image from an inline Dockerfile on AWS
CodeBuild, pushes it to ECR, and exposes the pushed image's **immutable digest**
so a downstream consumer pins to exactly the image that was built.

Five resources, one stack:

| Resource | Label | What it is for |
|---|---|---|
| `AWS::ECR::Repository` | `checkout-api-repo` | Where the image is pushed. |
| `AWS::Logs::LogGroup` | `checkout-api-build-logs` | Where the build's console output goes. |
| `AWS::IAM::Role` | `checkout-api-build-role` | The identity the build runs as. |
| `AWS::CodeBuild::Project` | `checkout-api-image-builder` | The compute the build runs on. |
| `AWS::CodeBuild::ImageBuild` | `checkout-api-image` | The build itself — one build, one push, one digest. |

## How they are wired

Nothing is joined up by naming convention. Every join is a resolvable, so
formae derives the apply order from the graph and no ARN, registry hostname or
project name is written out by hand:

```
ECR::Repository ──res.arn──────────► IAM::Role   (ECR push statement)
                └─res.repositoryUri─────────────────► CodeBuild::ImageBuild

Logs::LogGroup  ──res.arn──────────► IAM::Role   (log-write statement)
                └─res.logGroupName─► CodeBuild::Project (logsConfig)

IAM::Role       ──res.arn──────────► CodeBuild::Project (serviceRole)

CodeBuild::Project ─res.name───────► CodeBuild::ImageBuild (projectName)
```

The `ImageBuild` is the leaf: nothing depends on it inside the stack, and it in
turn depends on the repository it pushes to and the project it runs on.

## Why all four supporting resources are declared

`AWS::CodeBuild::ImageBuild` is a **pure build-and-push action**. It creates no
AWS resource of its own beyond the image it pushes: no IAM role, no CodeBuild
project, no log group. That is deliberate, and it is why this example is five
resources rather than one.

Yes, declaring them is more verbose than a resource that conjures its own
scaffolding. The verbosity buys things that matter:

- **Nothing exists that you did not ask for.** An audit of the account against
  this forma comes out even. There is no undeclared IAM role with ECR write
  permissions sitting in the account under a name you would have to reverse
  engineer to find.
- **The role is yours to tighten.** The policy in `main.pkl` is scoped to one
  repository and one log group. If your organisation requires a permissions
  boundary, a specific path, or a tag, you add it — you are not arguing with a
  policy generated inside a plugin.
- **The log group is yours to configure.** A log group CodeBuild creates for
  itself has no retention period, so build logs accumulate forever. The declared
  group here sets `retentionInDays = 30`, and can take tags or a KMS key.
- **The project is yours to size and share.** Compute size, builder image and
  build timeout are Project properties. Swapping `BUILD_GENERAL1_SMALL` for a
  larger size, or moving to an ARM builder, is an edit in the forma rather than
  a property of a hidden project.
- **Teardown is honest.** Destroying the stack destroys exactly these five
  things. Nothing is left behind because it was never in the state.

## The Project's build spec is a placeholder

The Project declares an inline `source.buildSpec` that does nothing but `echo`.
That is correct and intentional.

`ImageBuild` supplies the spec it actually runs **per build**, as a build-spec
override on `StartBuild`. It never reads, writes or depends on the project's own
spec. The declared spec exists only because CodeBuild rejects a `NO_SOURCE`
project that carries no inline spec at all. Put anything valid there; it will
not run.

For the same reason, the image is built from the `dockerfile` property alone.
There is no source repository and no build context, so a `COPY` from the working
directory has nothing to copy — bring files in through your base image or fetch
them in a `RUN` step.

## What the Project must look like

Four settings are checked before any build starts, and a project that does not
satisfy them is rejected immediately with a message naming the offending value
rather than after a build attempt:

| Setting | Required value | Why |
|---|---|---|
| `environment.type` | `LINUX_CONTAINER` | The generated spec is a Linux shell script. |
| `environment.privilegedMode` | `true` | The build runs `docker` itself. |
| `source.type` | `NO_SOURCE` | Everything the build needs arrives per build. |
| `artifacts.type` | `NO_ARTIFACTS` | The result is the pushed image, not an artifact. |

Everything else about the project — its size, its builder image, its timeout,
its cache — is yours.

Some preconditions deliberately are *not* checked, because checking them cheaply
is not possible: whether the project's network placement can reach ECR, STS and
CloudWatch Logs, and whether the service role's policy really grants the pushes.
Those surface as real AWS errors from the build.

## The `LOCAL_DOCKER_LAYER_CACHE` caveat

The example declares `cache { type = "NO_CACHE" }`. CodeBuild also offers a
local cache:

```pkl
cache {
  type = "LOCAL"
  modes {
    "LOCAL_DOCKER_LAYER_CACHE"
  }
}
```

That can make repeated builds substantially faster — and it is **only safe on a
project dedicated to a single image build**.

The local Docker layer cache lives on the build host and is scoped to the
*project*, not to the build. Every build on a project can reuse the layers left
behind by every other build on it, and reuse is keyed on the instruction rather
than on which build ran it. Two things follow, and neither is visible in the
resulting image's declared inputs:

- **Stale content leaks across builds.** A non-deterministic instruction
  (`RUN apk add …`, `RUN curl …`) that another build already ran is served from
  that build's layer instead of being re-run, so the image can carry what the
  *other* build fetched, at the time it fetched it.
- **Secrets leak across builds.** A value written into an intermediate layer by
  one build — an `ARG`-supplied token, a credential file deleted again in a
  later instruction — stays in the shared cache, where another build on the same
  project can reach it.

So:

- **One project per image build** — enable the layer cache freely.
- **A project shared by several image builds** — leave it `NO_CACHE`.

Sharing a project is otherwise supported and reasonable: several `ImageBuild`
resources may name the same `projectName`, teardown of one stops only the builds
that resource started, and no `ImageBuild` ever deletes the project.

## Consuming the result

The build exports three resolvables. Consume the **digest**, not the tag:

```pkl
// Immutable — pins to exactly the image this build produced.
image = appImage.res.imageRef        // <repo>@sha256:…

// Also available:
appImage.res.imageDigest             // sha256:… alone
appImage.res.imageUri                // <repo>:<tag> — mutable, convenience only
```

`imageRef` is the reason to build inside formae at all: an ECS task definition
or a Kubernetes pod spec that resolves it is pinned to a specific build, and a
new build is a change to that consumer rather than an invisible re-tag.

Rebuilds are gated on a hash over everything that determines what gets built —
the Dockerfile, the build args, the build-spec generator, and a fingerprint of
the project the build runs on (its name, environment type, compute type, builder
image, privileged mode, project-level environment variables and cache type).
Re-applying an unchanged forma does not rebuild. Changing the Dockerfile, a
build arg, or the project's builder image does.

## Running it

Prerequisites: a running formae agent with credentials for the target account,
and the AWS plugin installed.

Edit the four values at the top of `main.pkl` — `awsRegion`, `appName`,
`releaseTag`, and `dockerfileContent` — then:

```bash
formae apply examples/codebuild-image-build/main.pkl
```

The first apply creates the repository, log group, role and project, then runs
the build. Build output is in the declared log group; the resource reports the
build's progress while it runs.

```bash
formae destroy examples/codebuild-image-build/main.pkl
```

Teardown removes the image this build pushed (scoped to its own tag) and stops
any of its in-flight builds, then destroys the project, role, log group and
repository.

## Notes on adapting it

- **Immutable repositories.** Setting `imageTagMutability = "IMMUTABLE"` on the
  repository is a good fit for the digest-pinning pattern, but it means a given
  tag can be pushed exactly once. Bump `releaseTag` for every change to the
  Dockerfile or build args; re-pushing an existing tag fails at the registry.
- **Build args as inputs, not secrets.** `buildArgs` values are part of the
  build's hashed inputs and are visible in the build's environment. Fetch
  secrets inside the build from Secrets Manager or Parameter Store instead.
- **Region.** The ECR repository must be in the same account and region as the
  build project; a mismatch is rejected before the build starts.
- **The role's ECR read actions.** `BatchGetImage` and `GetDownloadUrlForLayer`
  are the pull half of the push handshake, and are what a Dockerfile whose
  `FROM` points at this same repository would need. Drop them only if you are
  sure of both.
