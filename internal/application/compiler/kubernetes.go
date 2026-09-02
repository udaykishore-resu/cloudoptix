package compiler

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// ParseKubernetesManifest reads a multi-document Kubernetes YAML stream
// (documents separated by "---", exactly what `kubectl apply -f` and
// `helm template` both produce) and prices Deployments, StatefulSets,
// DaemonSets against their containers' resource requests, and folds in any
// HorizontalPodAutoscaler that targets one of them.
//
// A manifest carries no before/after state either — like raw HCL and a bare
// CloudFormation template, it is what you are about to apply, not a diff —
// so every workload is emitted as ChangeCreate.
//
// Node cost basis: a manifest names no EC2 instance type, cluster or
// bin-packing strategy, so there is no way to price "how much of a node this
// pod costs" without inventing a node shape. Instead each vCPU and GiB of
// requested capacity is priced at the Fargate on-demand vCPU-hour / GB-hour
// rate from the pricing catalog (ports.PricingCatalog.ServicePrice("fargate",
// "vcpu_hour"/"gb_hour")) — a real, published per-unit-of-capacity price that
// does not depend on which node type or how tightly pods are packed onto it.
// This is reported as a stated Assumption on every such PricedChange, because
// EKS-on-EC2 with efficient bin-packing is usually cheaper than this basis
// and an over-provisioned or poorly bin-packed cluster can be considerably
// more expensive; Fargate-equivalent pricing is the defensible middle
// estimate, not a claim about the actual node bill.
func ParseKubernetesManifest(content []byte, fallbackRegion core.Region) ([]RawResource, []string, error) {
	docs, warnings, err := decodeYAMLDocs(content)
	if err != nil {
		return nil, nil, err
	}
	if len(docs) == 0 {
		return nil, nil, fmt.Errorf("compiler: manifest contains no Kubernetes objects")
	}

	var workloads []RawResource
	// hpaByTarget maps "kind/name" of a scaleTargetRef onto the HPA's
	// min/max replica bounds, applied to the matching workload below.
	hpaByTarget := map[string]k8sHPA{}

	for _, doc := range docs {
		kind := doc.Str("kind", "")
		name := Attrs(doc.Map("metadata")).Str("name", "")
		namespace := Attrs(doc.Map("metadata")).Str("namespace", "default")
		if kind == "" || name == "" {
			continue
		}
		switch kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			w, wwarn := k8sWorkloadToRaw(kind, namespace, name, doc)
			warnings = append(warnings, wwarn...)
			workloads = append(workloads, w)
		case "HorizontalPodAutoscaler":
			spec := doc.Map("spec")
			target := Attrs(spec.Map("scaleTargetRef"))
			key := target.Str("kind", "") + "/" + target.Str("name", "")
			hpaByTarget[key] = k8sHPA{
				min: spec.Int("minReplicas", 1),
				max: spec.Int("maxReplicas", spec.Int("minReplicas", 1)),
			}
		default:
			// Every other kind (Service, ConfigMap, Secret, Ingress, RBAC,
			// CRDs, ...) carries no capacity request this compiler can price
			// and is silently omitted from the change set rather than listed
			// as Unpriced — a Service has no resource_changes-style presence
			// in a Terraform plan either, and cluttering the report with
			// zero-information rows for every ConfigMap would bury the
			// workloads that matter.
		}
	}

	for i, w := range workloads {
		kind := capitalizeK8sKind(strings.TrimPrefix(w.Type, "k8s_"))
		if hpa, ok := hpaByTarget[kind+"/"+lastPathSegment(w.Address)]; ok {
			w.After["hpa_min_replicas"] = float64(hpa.min)
			w.After["hpa_max_replicas"] = float64(hpa.max)
			workloads[i] = w
		}
	}

	for i := range workloads {
		workloads[i].Region = fallbackRegion
	}
	return workloads, warnings, nil
}

type k8sHPA struct{ min, max int }

// k8sLabels reads a Kubernetes object's metadata.labels — Kubernetes' tag
// equivalent — into the flat map the untagged-resource CostRisk and the
// require_tags regression check both expect (via RawResource.Tags), rather
// than Attrs.Tags(), which looks for a "tags" key that Kubernetes objects
// never have.
func k8sLabels(metadata Attrs) map[string]string {
	m := metadata.Map("labels")
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := asString(v); ok {
			out[k] = s
		}
	}
	return out
}

func capitalizeK8sKind(k string) string {
	switch k {
	case "deployment":
		return "Deployment"
	case "statefulset":
		return "StatefulSet"
	case "daemonset":
		return "DaemonSet"
	default:
		return k
	}
}

func lastPathSegment(address string) string {
	i := strings.LastIndex(address, "/")
	if i < 0 {
		return address
	}
	return address[i+1:]
}

// k8sWorkloadToRaw sums container resource requests across a workload's pod
// template and multiplies by its replica count (Deployment/StatefulSet) or
// leaves the per-pod total alone for a DaemonSet, whose replica count is
// "one per matching node" — a quantity this compiler cannot know without a
// live cluster, surfaced as an overridable Assumption rather than guessed.
func k8sWorkloadToRaw(kind, namespace, name string, doc Attrs) (RawResource, []string) {
	var warnings []string
	spec := doc.Map("spec")
	podSpec := Attrs(Attrs(spec.Map("template")).Map("spec"))
	containers := podSpec.List("containers")

	var totalCPU, totalMemBytes float64
	for _, c := range containers {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		requests := Attrs(Attrs(cm).Map("resources")).Map("requests")
		if requests == nil {
			warnings = append(warnings, fmt.Sprintf(
				"%s/%s: a container declares no resources.requests; it contributes zero to this workload's priced capacity", kind, name))
			continue
		}
		if cpuStr := requests.Str("cpu", ""); cpuStr != "" {
			if v, ok := parseK8sQuantity(cpuStr); ok {
				totalCPU += v
			}
		}
		if memStr := requests.Str("memory", ""); memStr != "" {
			if v, ok := parseK8sQuantity(memStr); ok {
				totalMemBytes += v
			}
		}
	}

	replicas := spec.Int("replicas", 1)
	if kind == "DaemonSet" {
		replicas = 0 // resolved by the pricer against an assumed node count
	}

	after := Attrs{
		"vcpu_request":       totalCPU,
		"memory_gib_request": totalMemBytes / (1 << 30),
		"replicas":           float64(replicas),
		"kind":               kind,
		"namespace":          namespace,
	}
	return RawResource{
		Address: fmt.Sprintf("%s/%s/%s", kind, namespace, name),
		Type:    "k8s_" + strings.ToLower(kind),
		Action:  simulate.ChangeCreate,
		After:   after,
		Tags:    k8sLabels(doc.Map("metadata")),
	}, warnings
}

// decodeYAMLDocs reads every document in a "---"-separated YAML stream,
// which is the shape both a hand-written multi-resource manifest and
// `helm template`'s rendered output take — Helm's own "# Source:" comment
// markers between documents are ordinary YAML comments and need no special
// handling.
func decodeYAMLDocs(content []byte) ([]Attrs, []string, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(content)))
	var docs []Attrs
	var warnings []string
	for {
		var v map[string]any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("compiler: invalid Kubernetes YAML: %w", err)
		}
		if len(v) == 0 {
			continue // an empty document between "---" separators
		}
		docs = append(docs, Attrs(v))
	}
	return docs, warnings, nil
}
