# Try it

A hands-on tour, from a quick offline run to enforcing the rules in a local cluster. Each part stands on its own, so skip what you don't need.

> `--values`, the baseline and the config file need **v0.6.0 or newer** (`k8s-guardian version`).

## 1. Install

```bash
go install github.com/andronaft/k8s-guardian/cmd/k8s-guardian@latest
# or: docker run --rm -v "$PWD:/work" ghcr.io/andronaft/k8s-guardian check -f .
# or: a binary from https://github.com/andronaft/k8s-guardian/releases

git clone https://github.com/andronaft/k8s-guardian && cd k8s-guardian   # for the examples
```

## 2. Offline, with the examples

No cluster and no API key needed.

```bash
k8s-guardian check -f examples/insecure-deployment.yaml      # 6 errors, exit code 1
k8s-guardian check -f examples/secure-deployment.yaml        # ✔ no issues found, exit code 0
```

Fix it. `--stdout` prints the result instead of rewriting the file:

```bash
k8s-guardian check -f examples/insecure-deployment.yaml --fix --stdout
# adds seccompProfile: RuntimeDefault, the one safe fix here

k8s-guardian check -f examples/insecure-deployment.yaml --fix --unsafe-fixes --stdout
# also runAsNonRoot, readOnlyRootFilesystem, requests and limits: review before applying
```

Your organisation's own rules, written in YAML:

```bash
k8s-guardian check -f examples/ --rules examples/rules       # reports ORG001, ORG002, ORG004
```

## 3. Your own repository: baseline and config

On an existing repository the first run usually finds a lot. Record what is there today, and fail only on new problems:

```bash
cd ~/src/my-service
k8s-guardian check -f k8s/                                   # exit 1: the backlog
k8s-guardian check -f k8s/ --update-baseline                 # writes .k8s-guardian-baseline.json
printf 'baseline: .k8s-guardian-baseline.json\n' > .k8s-guardian.yaml
k8s-guardian check -f k8s/                                   # exit 0: "N known finding(s) hidden by baseline"
```

Now add a manifest with a problem, e.g. an `image: …:latest`. The check reports only the new file and exits with 1. Commit both files so CI uses the same baseline. More settings: [configuration](configuration.md).

## 4. Your Helm chart, with your values

```bash
k8s-guardian check -f charts/my-app                                   # default values
k8s-guardian check -f charts/my-app --values values-prod.yaml         # what you deploy
k8s-guardian check -f charts/my-app --values values-prod.yaml --set replicaCount=3
```

For example, with [podinfo](https://github.com/stefanprodan/podinfo): with default values KG015 reports `spec.replicas is 1`, and with `--set replicaCount=3` it doesn't. Add `--kube-version 1.33` to render for a specific Kubernetes version. With `--live`, the cluster's version is used.

## 5. Against a local cluster

Any cluster works. These steps use [kind](https://kind.sigs.k8s.io/):

```bash
kind create cluster
kubectl apply -f examples/cluster/shop.yaml      # namespace "shop" with a ResourceQuota and a LimitRange
```

**Will it fit?** `--live` reads quotas, LimitRanges, nodes and references from the cluster (read-only):

```bash
k8s-guardian check -f examples/costly-app.yaml --live
```

```text
  ✖ error   LV005  container "api": requests.cpu 4 exceeds LimitRange "shop-limits" max 2
  ✖ error   LV005  container "api": limits.memory 8Gi exceeds LimitRange "shop-limits" max 4Gi
  ✖ error   LV004  needs requests.cpu=16 more (4 replica(s)) but ResourceQuota "shop-compute" in namespace "shop" only has … left
```

Whether LV003 (node capacity) appears depends on how many CPUs your machine gives the kind node.

**What breaks on apply?** Deploy the example, change its selector and image, and diff:

```bash
kubectl apply -f examples/secure-deployment.yaml
sed -e 's/app: api/app: orders-api/g' -e 's/api:1.4.2/api:1.5.0/' examples/secure-deployment.yaml > changed.yaml
k8s-guardian diff -f changed.yaml
```

```text
~ Deployment/api [shop]  3 change(s)
  ✖ error   DF001  spec.selector is immutable (live: matchLabels: app: api): apply will fail; delete and recreate (downtime) …
  ℹ info    DF007  pod template changed: triggers a rolling restart of 3 pod(s)
```

`kubectl apply -f changed.yaml` indeed fails with `spec.selector: … field is immutable`.

**Enforce the same rules in the cluster** (Kubernetes ≥ 1.30):

```bash
k8s-guardian export vap --rules examples/rules > guardrails.yaml
kubectl apply -f guardrails.yaml
kubectl apply -f examples/insecure-deployment.yaml
```

```text
Warning: Validation failed for ValidatingAdmissionPolicy 'k8s-guardian-kg006-read-only-root-filesystem-workloads' …
The deployments "web" is invalid: : ValidatingAdmissionPolicy 'k8s-guardian-kg011-no-host-namespaces-workloads' … denied request
```

Errors are denied and warnings are shown, as in `check`. Clean up with `kind delete cluster`.

## 6. In CI

Add a workflow to a test repository and open a pull request that changes a manifest:

```yaml
# .github/workflows/k8s-guardian.yml
name: k8s-guardian
on: [pull_request]
permissions:
  contents: read
jobs:
  guardrails:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: andronaft/k8s-guardian@v0.8.0
        with:
          path: k8s/
```

Findings appear as annotations on the changed lines and in the job summary. A `.k8s-guardian.yaml` with a baseline in the repository root is used automatically. More options: [CI integration](ci.md).

## 7. With your coding agent

```bash
claude mcp add k8s-guardian -- k8s-guardian mcp
```

Then ask: *"Write a Deployment for an nginx-based web app in namespace shop and make it pass k8s-guardian."* The agent can validate and fix its own YAML. See [MCP server](mcp.md).

## Found something wrong?

* **A finding that is wrong for a valid manifest:** open an issue with the label `false positive` and include the YAML (remove secrets).
* **A fix that changed something it shouldn't:** please include the YAML before and after.

Both are the most useful reports there are. Every fixed false positive gets a regression test.

---

*All commands on this page were run against k8s-guardian `main` and a kube-apiserver 1.37. kind wasn't used: the quota's `status` (normally kept up to date by the cluster's controller manager) was set by hand.*
