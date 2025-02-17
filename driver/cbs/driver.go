package cbs

import (
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/dbdd4us/qcloudapi-sdk-go/metadata"
	"github.com/golang/glog"
	csicommon "github.com/kubernetes-csi/drivers/pkg/csi-common"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tencentcloud/kubernetes-csi-tencentcloud/driver/cbs/tags"
	"github.com/tencentcloud/kubernetes-csi-tencentcloud/driver/metrics"
	"github.com/tencentcloud/kubernetes-csi-tencentcloud/driver/util"
)

const (
	DriverName          = "com.tencent.cloud.csi.cbs"
	DriverVersion       = "v1.0.0"
	TopologyZoneKey     = "topology." + DriverName + "/zone"
	componentController = "controller"
	componentNode       = "node"
	ADDRESS             = "ADDRESS"
)

type Driver struct {
	client        kubernetes.Interface
	metadataStore util.CachePersister

	endpoint string
	region   string
	zone     string
	nodeID   string
	cbsUrl   string
	// TKE cluster ID
	clusterId         string
	componentType     string
	environmentType   string
	volumeAttachLimit int64
}

func NewDriver(endpoint, region, zone, nodeID, cbsUrl, clusterId, componentType, environmentType string, volumeAttachLimit int64, client kubernetes.Interface) *Driver {
	glog.Infof("Driver: %v version: %v", DriverName, DriverVersion)

	metadataClient := metadata.NewMetaData(http.DefaultClient)
	if region == "" {
		r, err := util.GetFromMetadata(metadataClient, metadata.REGION)
		if err != nil {
			glog.Fatal(err)
		}
		region = r
	}
	if zone == "" {
		z, err := util.GetFromMetadata(metadataClient, metadata.ZONE)
		if err != nil {
			glog.Fatal(err)
		}
		zone = z
	}

	if componentType == "" {
		if os.Getenv(ADDRESS) != "" {
			componentType = componentController
		} else {
			componentType = componentNode
		}
	}

	if nodeID == "" && os.Getenv(NodeNameKey) == "" && componentType == componentNode {
		n, err := util.GetFromMetadata(metadataClient, metadata.INSTANCE_ID)
		if err != nil {
			glog.Fatal(err)
		}
		nodeID = n
	}

	return &Driver{
		client:            client,
		metadataStore:     util.NewCachePersister(),
		endpoint:          endpoint,
		region:            region,
		zone:              zone,
		nodeID:            nodeID,
		cbsUrl:            cbsUrl,
		clusterId:         clusterId,
		componentType:     componentType,
		environmentType:   environmentType,
		volumeAttachLimit: volumeAttachLimit,
	}
}

func (drv *Driver) Run(enableMetricsServer bool, metricPort int64, timeInterval int) {
	s := csicommon.NewNonBlockingGRPCServer()
	var cs *cbsController
	var ns *cbsNode

	glog.Infof("Specify component type: %s", drv.componentType)
	switch drv.componentType {
	case componentController:
		cs = newCbsController(drv)
	case componentNode:
		ns = newCbsNode(drv)
	}

	if cs != nil {
		if err := cs.LoadExDataFromMetadataStore(); err != nil {
			glog.Fatalf("failed to load metadata from store, err %v\n", err)
		}
	}

	if enableMetricsServer {
		// expose driver metrics
		metrics.RegisterMetrics()
		http.Handle("/metrics", promhttp.Handler())
		address := fmt.Sprintf(":%d", metricPort)
		glog.Infof("Starting metrics server at %s\n", address)
		go wait.Forever(func() {
			err := http.ListenAndServe(address, nil)
			if err != nil {
				glog.Errorf("Failed to listen on %s: %v", address, err)
			}
		}, 5*time.Second)
	}

	// Sync the tags of cluster and disks
	if drv.componentType == componentController {
		go func() {
			for {
				rand.Seed(time.Now().UnixNano())
				n := rand.Intn(timeInterval)
				glog.Infof("Begin to sync the tags of cluster and disks after sleeping %d minutes...\n", n)
				time.Sleep(time.Duration(n) * time.Minute)
				tags.UpdateDisksTags(drv.client, cs.cbsClient, cs.cvmClient, cs.tagClient, drv.region, drv.clusterId)
			}
		}()
	}

	s.Start(drv.endpoint, newCbsIdentity(), cs, ns)
	s.Wait()
}
