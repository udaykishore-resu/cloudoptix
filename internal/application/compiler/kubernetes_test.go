package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const sampleManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout
  namespace: prod
  labels:
    team: payments
spec:
  replicas: 4
  template:
    spec:
      containers:
      - name: app
        resources:
          requests:
            cpu: "500m"
            memory: "512Mi"
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: log-shipper
spec:
  template:
    spec:
      containers:
      - name: shipper
        resources:
          requests:
            cpu: "100m"
            memory: "128Mi"
---
apiVersion: v1
kind: Service
metadata:
  name: checkout-svc
spec:
  selector:
    app: checkout
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: checkout-hpa
spec:
  scaleTargetRef:
    kind: Deployment
    name: checkout
  minReplicas: 2
  maxReplicas: 10
`

func TestParseKubernetesManifest(t *testing.T) {
	raws, _, err := ParseKubernetesManifest([]byte(sampleManifest), "us-east-1")
	require.NoError(t, err)

	// The Service produces no RawResource (it has no priceable capacity).
	require.Len(t, raws, 2)

	byAddr := map[string]RawResource{}
	for _, r := range raws {
		byAddr[r.Address] = r
	}

	deploy := byAddr["Deployment/prod/checkout"]
	assert.Equal(t, "k8s_deployment", deploy.Type)
	assert.Equal(t, simulate.ChangeCreate, deploy.Action)
	assert.Equal(t, 0.5, deploy.After.Float("vcpu_request", 0))
	assert.InDelta(t, 512.0/1024, deploy.After.Float("memory_gib_request", 0), 0.0001)
	// The HPA overrides the plain replica count with its min/max bounds.
	assert.Equal(t, 2.0, deploy.After.Float("hpa_min_replicas", -1))
	assert.Equal(t, 10.0, deploy.After.Float("hpa_max_replicas", -1))
	assert.Equal(t, map[string]string{"team": "payments"}, deploy.Tags)

	ds := byAddr["DaemonSet/default/log-shipper"]
	assert.Equal(t, "k8s_daemonset", ds.Type)
	assert.Equal(t, 0.1, ds.After.Float("vcpu_request", 0))
}

func TestParseKubernetesQuantities(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"500m", 0.5, true},
		{"2", 2, true},
		{"128Mi", 128 * (1 << 20), true},
		{"1Gi", 1 << 30, true},
		{"100M", 100e6, true},
		{"", 0, false},
		{"not-a-number", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseK8sQuantity(tt.in)
		assert.Equal(t, tt.ok, ok, tt.in)
		if tt.ok {
			assert.InDelta(t, tt.want, got, 0.001, tt.in)
		}
	}
}

// TestCompile_KubernetesPricingFromResourceRequests is the end-to-end check
// that a Deployment's priced capacity is proportional to its resource
// requests and its replica count, using the Fargate-equivalent capacity
// basis documented on ParseKubernetesManifest.
func TestCompile_KubernetesPricingFromResourceRequests(t *testing.T) {
	c := New(pricing.New())
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: app
        resources:
          requests:
            cpu: "1"
            memory: "2Gi"
`
	result, err := c.Compile("t1", ports.CompileRequest{
		Source: simulate.SourceKubernetes, Region: "us-east-1", Content: []byte(manifest),
	})
	require.NoError(t, err)
	require.Len(t, result.Changes, 1)
	ch := result.Changes[0]
	assert.False(t, ch.Unpriced)
	assert.True(t, ch.AfterMonthly.GreaterThan(core.ZeroUSD()))

	// Doubling replicas must double the priced capacity cost exactly, since
	// the pricing basis is purely proportional to requested vCPU/GiB times
	// replica count.
	manifest4 := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  replicas: 4
  template:
    spec:
      containers:
      - name: app
        resources:
          requests:
            cpu: "1"
            memory: "2Gi"
`
	result4, err := c.Compile("t1", ports.CompileRequest{
		Source: simulate.SourceKubernetes, Region: "us-east-1", Content: []byte(manifest4),
	})
	require.NoError(t, err)
	ratio := result4.Changes[0].AfterMonthly.Ratio(ch.AfterMonthly)
	assert.InDelta(t, 2.0, ratio, 0.001)
}

// TestCompile_HelmRenderedOutputIsPricedLikeKubernetes confirms the
// SourceHelmRelease path reuses the Kubernetes manifest parser: Helm's own
// "# Source:" comment markers between documents are ordinary YAML comments.
func TestCompile_HelmRenderedOutputIsPricedLikeKubernetes(t *testing.T) {
	c := New(pricing.New())
	rendered := `---
# Source: chart/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: web
        resources:
          requests:
            cpu: "250m"
            memory: "256Mi"
`
	result, err := c.Compile("t1", ports.CompileRequest{
		Source: simulate.SourceHelmRelease, Region: "us-east-1", Content: []byte(rendered),
	})
	require.NoError(t, err)
	require.Len(t, result.Changes, 1)
	assert.False(t, result.Changes[0].Unpriced)
	assert.True(t, result.Changes[0].AfterMonthly.GreaterThan(core.ZeroUSD()))
}
