package controller

import (
	"cp-remote-access-api/internal/vault"
	"cp-remote-access-api/model"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"log"
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

func ExecWebSocketHandler(c *gin.Context) {
	var pod = c.Query("pod")
	var namespace = c.Query("namespace")
	var container = c.Query("container")
	var clusterId = c.Query("clusterId")

	val, exists := c.Get("claims")
	if !exists {
		log.Fatalf("Claims 조회 실패")
	}
	claims, exists := val.(jwt.MapClaims)
	if !exists {
		log.Fatalf("Claims 조회 실패")
	}

	clusterInfo, err := GetClusterInfo(clusterId, claims["userAuthId"].(string), claims["userType"].(string))
	if err != nil {
		log.Fatalf("클러스터 조회 실패: %v", err)
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

	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to load kubeconfig"))
		return
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to create clientset"))
		return
	}

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

	executor, err := remotecommand.NewWebSocketExecutor(cfg, "POST", req.URL().String())
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
