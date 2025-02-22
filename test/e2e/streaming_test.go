// +build streaming

package e2e

import (
	"context"
	"fmt"
	"testing"

	framework "github.com/operator-framework/operator-sdk/pkg/test"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/jaegertracing/jaeger-operator/pkg/apis/jaegertracing/v1"
)

type StreamingTestSuite struct {
	suite.Suite
}

func (suite *StreamingTestSuite) SetupSuite() {
	t = suite.T()
	var err error
	ctx, err = prepare(t)
	if err != nil {
		if ctx != nil {
			ctx.Cleanup()
		}
		require.FailNow(t, "Failed in prepare")
	}
	fw = framework.Global
	namespace, _ = ctx.GetNamespace()
	require.NotNil(t, namespace, "GetNamespace failed")

	addToFrameworkSchemeForSmokeTests(t)
}

func (suite *StreamingTestSuite) TearDownSuite() {
	handleSuiteTearDown()
}

func TestStreamingSuite(t *testing.T) {
	suite.Run(t, new(StreamingTestSuite))
}

func (suite *StreamingTestSuite) SetupTest() {
	t = suite.T()
}

func (suite *StreamingTestSuite) AfterTest(suiteName, testName string) {
	handleTestFailure()
}

func (suite *StreamingTestSuite) TestStreaming() {
	waitForElasticSearch()
	waitForKafkaInstance()

	j := jaegerStreamingDefinition(namespace, "simple-streaming")
	log.Infof("passing %v", j)
	err := fw.Client.Create(context.TODO(), j, &framework.CleanupOptions{TestContext: ctx, Timeout: timeout, RetryInterval: retryInterval})
	require.NoError(t, err, "Error deploying jaeger")
	defer undeployJaegerInstance(j)

	err = WaitForDeployment(t, fw.KubeClient, namespace, "simple-streaming-ingester", 1, retryInterval, timeout)
	require.NoError(t, err, "Error waiting for ingester deployment")

	err = WaitForDeployment(t, fw.KubeClient, namespace, "simple-streaming-collector", 1, retryInterval, timeout)
	require.NoError(t, err, "Error waiting for collector deployment")

	err = WaitForDeployment(t, fw.KubeClient, namespace, "simple-streaming-query", 1, retryInterval, timeout)
	require.NoError(t, err, "Error waiting for query deployment")

	ProductionSmokeTest("simple-streaming")
}

func (suite *StreamingTestSuite) TestStreamingWithTLS() {
	if !usingOLM {
		t.Skip("This test should only run when using OLM")
	}
	// Make sure ES and the kafka instance are available
	waitForElasticSearch()
	waitForKafkaInstance()

	kafkaUserName := "my-user"
	kafkaUser := getKafkaUser(kafkaUserName, kafkaNamespace)
	err := fw.Client.Create(context.Background(), kafkaUser, &framework.CleanupOptions{TestContext: ctx, Timeout: timeout, RetryInterval: retryInterval})
	require.NoError(t, err, "Error deploying kafkauser")
	WaitForSecret(kafkaUserName, kafkaNamespace)

	defer func() {
		if !debugMode || !t.Failed() {
			err = fw.Client.Delete(context.TODO(), kafkaUser)
			require.NoError(t, err)
		}
	}()

	// Now create a jaeger instance with TLS enabled -- note it has to be deployed in the same namespace as the kafka instance
	jaegerInstanceName := "tls-streaming"
	jaegerInstance := jaegerStreamingDefinitionWithTLS(kafkaNamespace, jaegerInstanceName, kafkaUserName)
	err = fw.Client.Create(context.TODO(), jaegerInstance, &framework.CleanupOptions{TestContext: ctx, Timeout: timeout, RetryInterval: retryInterval})
	require.NoError(t, err, "Error deploying jaeger")
	defer undeployJaegerInstance(jaegerInstance)

	err = WaitForDeployment(t, fw.KubeClient, kafkaNamespace, jaegerInstanceName+"-ingester", 1, retryInterval, timeout)
	require.NoError(t, err, "Error waiting for ingester deployment")

	err = WaitForDeployment(t, fw.KubeClient, kafkaNamespace, jaegerInstanceName+"-collector", 1, retryInterval, timeout)
	require.NoError(t, err, "Error waiting for collector deployment")

	err = WaitForDeployment(t, fw.KubeClient, kafkaNamespace, jaegerInstanceName+"-query", 1, retryInterval, timeout)
	require.NoError(t, err, "Error waiting for query deployment")

	ProductionSmokeTestWithNamespace(jaegerInstanceName, kafkaNamespace)
}

func jaegerStreamingDefinition(namespace string, name string) *v1.Jaeger {
	kafkaClusterURL := fmt.Sprintf("my-cluster-kafka-brokers.%s:9092", kafkaNamespace)
	j := &v1.Jaeger{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Jaeger",
			APIVersion: "jaegertracing.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple-streaming",
			Namespace: namespace,
		},
		Spec: v1.JaegerSpec{
			Strategy: "streaming",
			Collector: v1.JaegerCollectorSpec{
				Options: v1.NewOptions(map[string]interface{}{
					"kafka.producer.topic":   "jaeger-spans",
					"kafka.producer.brokers": kafkaClusterURL,
				}),
			},
			Ingester: v1.JaegerIngesterSpec{
				Options: v1.NewOptions(map[string]interface{}{
					"kafka.consumer.topic":   "jaeger-spans",
					"kafka.consumer.brokers": kafkaClusterURL,
				}),
			},
			Storage: v1.JaegerStorageSpec{
				Type: "elasticsearch",
				Options: v1.NewOptions(map[string]interface{}{
					"es.server-urls": esServerUrls,
				}),
			},
		},
	}
	return j
}

func jaegerStreamingDefinitionWithTLS(namespace string, name, kafkaUserName string) *v1.Jaeger {
	volumes := getTLSVolumes(kafkaUserName)
	volumeMounts := getTLSVolumeMounts()

	kafkaClusterURL := fmt.Sprintf("my-cluster-kafka-bootstrap.%s.svc.cluster.local:9093", kafkaNamespace)
	j := &v1.Jaeger{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Jaeger",
			APIVersion: "jaegertracing.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1.JaegerSpec{
			Strategy: "streaming",
			Collector: v1.JaegerCollectorSpec{
				Options: v1.NewOptions(map[string]interface{}{
					"kafka.producer.authentication": "tls",
					"kafka.producer.topic":          "jaeger-spans",
					"kafka.producer.brokers":        kafkaClusterURL,
					"kafka.producer.tls.ca":         "/var/run/secrets/cluster-ca/ca.crt",
					"kafka.producer.tls.cert":       "/var/run/secrets/kafkauser/user.crt",
					"kafka.producer.tls.key":        "/var/run/secrets/kafkauser/user.key",
				}),
			},
			Ingester: v1.JaegerIngesterSpec{
				Options: v1.NewOptions(map[string]interface{}{
					"kafka.consumer.authentication": "tls",
					"kafka.consumer.topic":          "jaeger-spans",
					"kafka.consumer.brokers":        kafkaClusterURL,
					"kafka.consumer.tls.ca":         "/var/run/secrets/cluster-ca/ca.crt",
					"kafka.consumer.tls.cert":       "/var/run/secrets/kafkauser/user.crt",
					"kafka.consumer.tls.key":        "/var/run/secrets/kafkauser/user.key",
					"ingester.deadlockInterval":     0,
				}),
			},
			Storage: v1.JaegerStorageSpec{
				Type: "elasticsearch",
				Options: v1.NewOptions(map[string]interface{}{
					"es.server-urls": esServerUrls,
				}),
			},
			JaegerCommonSpec: v1.JaegerCommonSpec{
				Volumes:      volumes,
				VolumeMounts: volumeMounts,
			},
		},
	}
	return j
}

func getKafkaUser(name, namespace string) *unstructured.Unstructured {
	kafkaUser := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kafka.strimzi.io/v1beta1",
			"kind":       "KafkaUser",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"strimzi.io/cluster": "my-cluster",
				},
			},
			"spec": map[string]interface{}{
				"authentication": map[string]interface{}{
					"type": "tls",
				},
			},
		},
	}

	return kafkaUser
}

func getTLSVolumeMounts() []corev1.VolumeMount {
	kafkaUserVolumeMount := corev1.VolumeMount{
		Name:      "kafkauser",
		MountPath: "/var/run/secrets/kafkauser",
	}
	clusterCaVolumeMount := corev1.VolumeMount{
		Name:      "cluster-ca",
		MountPath: "/var/run/secrets/cluster-ca",
	}

	volumeMounts := []corev1.VolumeMount{
		kafkaUserVolumeMount, clusterCaVolumeMount,
	}

	return volumeMounts
}

func getTLSVolumes(kafkaUserName string) []corev1.Volume {
	kafkaUserSecretName := corev1.SecretVolumeSource{
		SecretName: kafkaUserName,
	}
	clusterCaSecretName := corev1.SecretVolumeSource{
		SecretName: "my-cluster-cluster-ca-cert",
	}

	kafkaUserVolume := corev1.Volume{
		Name: "kafkauser",
		VolumeSource: corev1.VolumeSource{
			Secret: &kafkaUserSecretName,
		},
	}
	clusterCaVolume := corev1.Volume{
		Name: "cluster-ca",
		VolumeSource: corev1.VolumeSource{
			Secret: &clusterCaSecretName,
		},
	}

	volumes := []corev1.Volume{
		kafkaUserVolume,
		clusterCaVolume,
	}

	return volumes
}

func waitForKafkaInstance() {
	err := WaitForStatefulset(t, fw.KubeClient, kafkaNamespace, "my-cluster-kafka", retryInterval, timeout)
	require.NoError(t, err, "Error waiting for my-cluster-kafka")
}

func waitForElasticSearch() {
	err := WaitForStatefulset(t, fw.KubeClient, storageNamespace, "elasticsearch", retryInterval, timeout)
	require.NoError(t, err, "Error waiting for elasticsearch")
}
