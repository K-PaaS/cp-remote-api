package controller

import (
	"bytes"
	"context"
	"cp-remote-access-api/internal/vault"
	"cp-remote-access-api/model"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func GetClusterInfo(clusterID string, userAuthId string, userType string) (model.ClusterCredential, error) {
	vaultCfg := vault.ConfigFromEnv()
	vaultClient, err := vault.NewClient(vaultCfg)
	if err != nil {
		log.Fatalf("Vault 클라이언트 생성 실패: %v", err)
	}
	//clusterInfo, err := vaultClient.GetClusterInfo(clusterID)

	clusterAPI, err := vaultClient.GetClusterAPI(clusterID)
	clusterToken, err := vaultClient.GetClusterToken(clusterID, userAuthId, userType)
	var clusterInfo = model.ClusterCredential{ClusterID: clusterID, APIServerURL: clusterAPI, BearerToken: clusterToken}
	if err != nil {
		log.Fatalf("클러스터 정보 조회 실패: %v", err)
	}
	return clusterInfo, err
}

var newExecutor = func(clientset kubernetes.Interface, cfg *rest.Config, pod, namespace, container string) (remotecommand.Executor, error) {
	req := clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   []string{"/bin/sh"},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	return remotecommand.NewWebSocketExecutor(cfg, "POST", req.URL().String())
}

func ExecWebSocketHandler(c *gin.Context) {
	var pod = c.Query("pod")
	var namespace = c.Query("namespace")
	var container = c.Query("container")
	var clusterId = c.Query("clusterId")

	val, exists := c.Get("claims")
	if !exists {
		// log.Fatalf("Claims 조회 실패")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "claims not found"})
		return
	}
	//claims, ok := val.(jwt.MapClaims)
	//if !ok {
	//	// log.Fatalf("Claims 조회 실패")
	//	c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid claims format"})
	//	return
	//}

	claims := val.(jwt.MapClaims)

	clusterInfo, err := GetClusterInfo(clusterId, claims["userAuthId"].(string), claims["userType"].(string))
	if err != nil {
		// log.Fatalf("클러스터 조회 실패: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get cluster info: " + err.Error()})
		return
	}
	cfg := &rest.Config{
		Host:        clusterInfo.APIServerURL,
		BearerToken: clusterInfo.BearerToken,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	}
	Upgrader.Subprotocols = []string{"bearer"}

	conn, err := Upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		return
	}
	defer conn.Close()

	//clientset, err := kubernetes.NewForConfig(cfg)
	clientset, err := K8sClientFactoryImpl.NewForConfig(cfg)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to create clientset"))
		return
	}

	executor, err := newExecutor(clientset, cfg, pod, namespace, container)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Executor error:"+err.Error()))
		return
	}

	wsStream := newWebSocketStream(conn)

	err = executor.Stream(remotecommand.StreamOptions{
		Stdin:  wsStream,
		Stdout: wsStream,
		Stderr: wsStream,
		Tty:    true,
	})
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Exec stream error: "+err.Error()))
	}
}

type ContainerShellStatus struct {
	Name     string `json:"name"`
	HasShell bool   `json:"hasShell"`
}

func CheckShellHandler(c *gin.Context) {
	var namespace = c.Query("namespace")
	var podName = c.Query("pod")
	var clusterId = c.Query("clusterId")

	val, exists := c.Get("claims")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Claims not found in context"})
		return
	}
	claims, exists := val.(jwt.MapClaims)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid claims format"})
		return
	}

	clusterInfo, err := GetClusterInfo(clusterId, claims["userAuthId"].(string), claims["userType"].(string))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get cluster info: " + err.Error()})
		return
	}
	cfg := &rest.Config{
		Host:        clusterInfo.APIServerURL,
		BearerToken: clusterInfo.BearerToken,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	}

	clientset, err := K8sClientFactoryImpl.NewForConfig(cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create clientset: " + err.Error()})
		return
	}

	podInfo, err := clientset.CoreV1().Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Pod not found: " + err.Error()})
		return
	}

	var statuses []ContainerShellStatus
	command := []string{"/bin/sh", "-c", "type /bin/sh"}

	for _, container := range podInfo.Spec.Containers {

		req := clientset.CoreV1().RESTClient().Post().
			Resource("pods").
			Name(podName).
			Namespace(namespace).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: container.Name,
				Command:   command,
				Stdin:     false,
				Stdout:    true,
				Stderr:    true,
				TTY:       false,
			}, scheme.ParameterCodec)

		var stdout, stderr bytes.Buffer
		executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
		if err != nil {
			statuses = append(statuses, ContainerShellStatus{
				Name:     container.Name,
				HasShell: false,
			})
			continue
		}
		err = executor.Stream(remotecommand.StreamOptions{
			Stdout: &stdout,
			Stderr: &stderr,
		})

		hasShellResult := (err == nil)

		statuses = append(statuses, ContainerShellStatus{
			Name:     container.Name,
			HasShell: hasShellResult,
		})
	}

	c.JSON(http.StatusOK, statuses)
}

type K8sClientFactory interface {
	NewForConfig(*rest.Config) (kubernetes.Interface, error)
}

type realK8sClientFactory struct{}

func (f *realK8sClientFactory) NewForConfig(c *rest.Config) (kubernetes.Interface, error) {
	return kubernetes.NewForConfig(c)
}

var K8sClientFactoryImpl K8sClientFactory = &realK8sClientFactory{}
