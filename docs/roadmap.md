# Roadmap

The plan lives in [GitHub milestones](https://github.com/andronaft/k8s-guardian/milestones) and issues. This page explains the priorities. Release notes for what already shipped are on the [releases page](https://github.com/andronaft/k8s-guardian/releases).

**Guiding principle:** a finding people can't trust is worse than no finding. Accuracy on real manifests comes before new features, and every new rule is run against [30 real Helm charts](real-world-test.md) before it ships.

## Shipped: [v0.8.0](https://github.com/andronaft/k8s-guardian/releases/tag/v0.8.0), trust in `--fix`, speed, easy adoption

`--fix` writes only the lines it adds, linear-time checks, DF009 for the API server's warnings, SARIF for code scanning, `k8s-guardian init`, the Action's `new-only` mode, LV003 with node capacity in use, KG019–KG021, more rules exported as admission policies, `rules --format json`, and Kustomize overlays with `helmCharts` (#5, #13, #14, #17, #19–#22, #27, #29).

## Next: [v0.9.0](https://github.com/andronaft/k8s-guardian/milestone/4), coverage

Check what matters most in the Kubernetes security model, and more of the workloads people actually run.

| Issue | |
|-------|---|
| [#12](https://github.com/andronaft/k8s-guardian/issues/12) | Pod Security Standards profiles: `--profile baseline\|restricted` |
| [#15](https://github.com/andronaft/k8s-guardian/issues/15) | Configurable workload kinds for CRDs (Knative, KubeVirt, …) |
| [#16](https://github.com/andronaft/k8s-guardian/issues/16) | Export Argo Rollouts and probe rules to VAP and Kyverno |
| [#26](https://github.com/andronaft/k8s-guardian/issues/26) | GitHub Action: post the findings as a pull request comment |
| [#30](https://github.com/andronaft/k8s-guardian/issues/30) | GitHub Action: use the release binary instead of building from source |
| [#28](https://github.com/andronaft/k8s-guardian/issues/28) | LV003: required pod anti-affinity and topology spread constraints |

## [Later](https://github.com/andronaft/k8s-guardian/milestone/3): ideas that need evidence first

| Issue | |
|-------|---|
| [#18](https://github.com/andronaft/k8s-guardian/issues/18) | Recommend HPA targets from Prometheus history (`cost`, experimental) |

## How to help

* **Try it on your manifests** ([try it](try-it.md)) and [report false positives](https://github.com/andronaft/k8s-guardian/issues/new?labels=false+positive). They are the most valuable reports.
* **Pick an issue:** [good first issues](https://github.com/andronaft/k8s-guardian/issues?q=is%3Aopen+label%3A%22good+first+issue%22) come with pointers to the code. [CONTRIBUTING.md](../CONTRIBUTING.md) explains how to add a rule.
* **Propose something:** open an issue first for larger changes, so we can agree on the approach before you write code.
