//go:build kubernetes

package jobs

import (
	"encoding/json"
	"fmt"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// InterventionKubernetesJob restarts or scales a Kubernetes workload.
type InterventionKubernetesJob struct {
	Execution
	EnqueueTime    time.Time
	StartTime      time.Time
	KubeconfigPath string
	Namespace      string
	Kind           string
	Name           string
	Replicas       *int
	Timeout        time.Duration
	Retries        int
	Entity         ecs.Entity
	ID             uuid.UUID
}

func newInterventionKubernetesJob(t *schema.InterventionTargetKubernetes, retries int, entity ecs.Entity) (Job, error) {
	job := &InterventionKubernetesJob{
		ID:             uuid.New(),
		Entity:         entity,
		KubeconfigPath: t.KubeconfigPath,
		Namespace:      t.Namespace,
		Kind:           t.Kind,
		Name:           t.Name,
		Timeout:        t.Timeout,
		Retries:        retries,
	}
	if t.Replicas != nil {
		v := *t.Replicas
		job.Replicas = &v
	}
	return job, nil
}

func (i *InterventionKubernetesJob) restConfig() (*rest.Config, error) {
	if i.KubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", i.KubeconfigPath)
	}
	return rest.InClusterConfig()
}

func (i *InterventionKubernetesJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(i.Context(), i.Timeout)
	defer cancel()

	payload := map[string]interface{}{"type": "intervention", "driver": "kubernetes"}
	cfg, err := i.restConfig()
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to build kubeconfig: %w", err), Payload: payload}
	}
	if cfg.ExecProvider != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("kubernetes exec credential providers cannot honor recovery deadlines; use token, certificate, or in-cluster credentials"), Payload: payload}
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to create kubernetes client: %w", err), Payload: payload}
	}

	if i.Kind != "deployment" {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("unsupported kubernetes kind %q (only deployment is supported)", i.Kind), Payload: payload}
	}
	deployments := clientset.AppsV1().Deployments(i.Namespace)
	if i.Replicas != nil {
		scale := &autoscalingv1.Scale{Spec: autoscalingv1.ScaleSpec{Replicas: int32(*i.Replicas)}}
		_, err = deployments.UpdateScale(ctx, i.Name, scale, metav1.UpdateOptions{})
	} else {
		// Rollout restart: patch the restartedAt pod-template annotation.
		patch := map[string]interface{}{
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"annotations": map[string]string{
							"kubectl.kubernetes.io/restartedAt": time.Now().Format(time.RFC3339),
						},
					},
				},
			},
		}
		data, merr := json.Marshal(patch)
		if merr != nil {
			return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to marshal restart patch: %w", merr), Payload: payload}
		}
		_, err = deployments.Patch(ctx, i.Name, types.StrategicMergePatchType, data, metav1.PatchOptions{})
	}
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("kubernetes intervention failed: %w", err), Payload: payload}
	}
	return Result{ID: i.ID, Ent: i.Entity, Err: nil, Payload: payload}
}

func (i *InterventionKubernetesJob) Copy() Job {
	job := *i
	if i.Replicas != nil {
		v := *i.Replicas
		job.Replicas = &v
	}
	return &job
}
func (i *InterventionKubernetesJob) GetEnqueueTime() time.Time  { return i.EnqueueTime }
func (i *InterventionKubernetesJob) SetEnqueueTime(t time.Time) { i.EnqueueTime = t }
func (i *InterventionKubernetesJob) GetStartTime() time.Time    { return i.StartTime }
func (i *InterventionKubernetesJob) SetStartTime(t time.Time)   { i.StartTime = t }
func (i *InterventionKubernetesJob) IsNil() bool                { return i == nil }
