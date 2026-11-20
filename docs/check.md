# Checking and fixing manifests

```bash
k8s-guardian check -f k8s/                      # files, directories, Helm charts, stdin (-f -)
k8s-guardian check -k overlays/prod             # Kustomize overlays (also: -f on a kustomization dir)
k8s-guardian check -f charts/app --values values-prod.yaml --set replicaCount=3
k8s-guardian check -f deploy.yaml --fix         # safe deterministic fixes, in place, comments kept
k8s-guardian check -f deploy.yaml --fix --unsafe-fixes  # + fixes that can change how the app runs
k8s-guardian check -f deploy.yaml --fix --ai    # + Claude for the rest (needs ANTHROPIC_API_KEY)
k8s-guardian audit deployment/my-app -n prod    # check live resources
```

```text
examples/insecure-deployment.yaml  Deployment/web
  ✖ error   KG011  hostNetwork enabled (line 17)
  ⚠ warning KG013  securityContext.seccompProfile is not set to RuntimeDefault or Localhost [fixable] (line 17)
  ✖ error   KG001  container "nginx": missing resources.requests (cpu, memory) [unsafe fix] (line 19)
  ✖ error   KG003  container "nginx": securityContext.runAsNonRoot is not true [unsafe fix] (line 19)
  ✖ error   KG004  container "nginx": securityContext.privileged is true (line 19)
  ✖ error   KG010  container "nginx": image "nginx:latest" uses the mutable :latest tag (line 19)
  ...
6 error(s), 4 warning(s), 2 info
1 issue(s) can be fixed automatically with --fix (add --ai to let Claude fix the rest)
4 more with --fix --unsafe-fixes: these can change how the workload runs (root images, writable filesystems, resources), so review them
```

## Inputs

* **Files and directories.** Directories are walked recursively. `node_modules`, `vendor`, dot-directories and symlinks are skipped, and YAML that isn't Kubernetes is skipped with a warning. A broken file that looks like a manifest fails the check. Use `exclude` in the [config file](configuration.md) to skip more.
* **Helm charts** (a directory with `Chart.yaml`) are rendered with `helm template`. For a chart passed with `-f`, `--values file` and `--set key=value` (both repeatable) are passed on, so you check what you actually deploy.
* **The Kubernetes version** charts are rendered for (`.Capabilities.KubeVersion`, `kubeVersion` constraints) comes from `--kube-version 1.33`, then `kubeVersion:` in the [config file](configuration.md), then the cluster's version when connected (`--live`, `diff`). Without any of them, the version built into your `helm` is used.
* **Kustomize** overlays are rendered with `kustomize build` or `kubectl kustomize`. Overlays that inflate Helm charts with `helmCharts` are rendered with `--enable-helm` when `helm` is in `PATH`; the chart values come from the kustomization (`valuesFile`, `valuesInline`), so `--values` and `--set` don't apply.
* **Workloads:** Pods, Deployments, StatefulSets, DaemonSets, ReplicaSets, ReplicationControllers, Jobs, CronJobs and Argo Rollouts.

## Fixes

`--fix` only applies fixes that can't break a working workload: `allowPrivilegeEscalation: false` and `seccompProfile: RuntimeDefault`.

`runAsNonRoot`, `readOnlyRootFilesystem`, `drop: [ALL]` and default resources are the right target, but they stop root images, apps that write to their filesystem and memory-hungry apps from running. They need `--unsafe-fixes` and a review, and the report marks them `[unsafe fix]`.

Privileged containers and host namespaces are never changed automatically, because node agents need them on purpose.

Fixes are idempotent and keep diffs small: a fix inserts the lines it adds and changes a value in place, so comments, formatting and everything else stay byte for byte (other documents, separators, Helm's `# Source:` lines). On the 30 charts of the [real-world test](real-world-test.md), `--fix` changes 134 lines in total, almost all of them added. Only when an edit can't be written this way (removing a key, extending a list) is the document re-encoded, in the list style it already uses. Rendered Helm charts and Kustomize overlays can't be rewritten, so their fixed YAML is printed instead. `--stdout` prints fixed YAML for any input.

To review each fix before it is applied, see [interactive review](interactive.md). For Claude-powered fixes, see [AI features](ai.md).

## Built-in rules

| ID | Name | Severity | Fix |
|----|------|----------|-----|
| KG001 | `resource-requests`: CPU and memory requests set | error | auto* |
| KG002 | `memory-limit`: memory limit set | error | auto* |
| KG003 | `run-as-non-root` | error | auto* |
| KG004 | `no-privileged` | error | AI |
| KG005 | `no-privilege-escalation` (not for privileged containers) | warning | auto |
| KG006 | `read-only-root-filesystem` | warning | auto* |
| KG007 | `drop-all-capabilities` (not for privileged containers) | warning | auto* |
| KG008 | `liveness-probe` (not for Jobs, CronJobs, run-once Pods, init containers) | warning | AI |
| KG009 | `readiness-probe` (not for Jobs, CronJobs, run-once Pods, init containers) | warning | AI |
| KG010 | `pinned-image-tag`: no `:latest` or untagged images | error | AI |
| KG011 | `no-host-namespaces` | error | AI |
| KG012 | `no-host-path` | warning | AI |
| KG013 | `seccomp-profile` | warning | auto |
| KG014 | `automount-service-account-token` | info | AI |
| KG015 | `high-availability`: at least 2 replicas, or an HPA with `minReplicas` ≥ 2 | info | AI |
| KG016 | `service-target-port`: Service selector is a near miss of a workload, or targetPort matches no declared containerPort | warning | AI |
| KG017 | `removed-api-version`: e.g. `extensions/v1beta1`, `batch/v1beta1` CronJob, `autoscaling/v2beta2` | error | AI |
| KG018 | `rbac-wildcard`: Roles and ClusterRoles granting `"*"` verbs or resources | warning | AI |
| KG019 | `no-host-port`: `ports[].hostPort` binds a node port (one such pod per node, bypasses NetworkPolicies on many CNIs; forbidden by the Pod Security baseline level) | warning | AI |
| KG021 | `literal-secret`: an env var that looks like a credential (`PASSWORD`, `SECRET`, `TOKEN`, `API_KEY`, `PRIVATE_KEY`) has a literal `value`; names that hold a path, a name or a setting (`_FILE`, `_NAME`, `_ENABLED`, …) and placeholder values are skipped, and the value is never printed | warning | AI |
| KG020 | `pod-disruption-budget`: a workload with 2+ replicas (or an HPA with `minReplicas` ≥ 2) has no PodDisruptionBudget in the manifest set selecting its pods, or one that allows no disruption (`maxUnavailable: 0`, `minAvailable` ≥ replicas) | info | AI |

auto: `--fix`. auto*: `--fix --unsafe-fixes` (review the result). AI: only `--fix --ai` or a manual edit. `k8s-guardian rules` prints this list, and `rules --format json` the same for tooling (ID, name, severity, description, scope and fix kind, custom rules included).

`k8s-guardian rules` lists every rule, including custom, live and diff rules. To write your own, see [custom rules](custom-rules.md).

## Ignoring findings

* For one resource: add the annotation `k8s-guardian.io/ignore: "KG011,no-host-path"`.
* Everywhere: `--skip KG015`, or `skip` in the [config file](configuration.md).
* To adopt k8s-guardian in a repository with many existing findings, record them in a [baseline](configuration.md#baseline) and fail only on new ones.

## Output and exit codes

`--format text|json|sarif|github|markdown`. Exit codes: `0` passed, `1` findings at or above `--fail-on` (default `error`), `2` usage or runtime error. For CI setups, see [CI integration](ci.md).
