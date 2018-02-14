package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"

	"github.com/atlassian/escalator/pkg/controller"
	"github.com/atlassian/escalator/pkg/k8s"
	"github.com/atlassian/escalator/pkg/metrics"
	"gopkg.in/alecthomas/kingpin.v2"

	log "github.com/sirupsen/logrus"
)

var (
	loglevel            = kingpin.Flag("loglevel", "Logging level passed into logrus. 4 for info, 5 for debug.").Short('v').Default(fmt.Sprintf("%d", log.InfoLevel)).Int()
	addr                = kingpin.Flag("address", "Address to listen to for /metrics").Default(":8080").String()
	scanInterval        = kingpin.Flag("scaninterval", "How often cluster is reevaluated for scale up or down").Default("60s").Duration()
	kubeConfigFile      = kingpin.Flag("kubeconfig", "Kubeconfig file location").String()
	nodegroupConfigFile = kingpin.Flag("nodegroups", "Config file for nodegroups nodegroups").Required().String()
	drymode             = kingpin.Flag("drymode", "master drymode argument. If true, forces drymode on all nodegroups").Bool()
)

func main() {
	kingpin.Parse()

	if *loglevel < 0 || *loglevel > 5 {
		log.Fatalf("Invalid log level %v provided. Must be between 0 (Critical) and 5 (Debug)", *loglevel)
	}
	log.SetLevel(log.Level(*loglevel))
	log.Infoln("Starting with log level", log.GetLevel())

	// if the kubeConfigFile is in the cmdline args then use the out of cluster config
	var k8sClient kubernetes.Interface
	if kubeConfigFile != nil && len(*kubeConfigFile) > 0 {
		log.Infoln("Using out of cluster config")
		k8sClient = k8s.NewOutOfClusterClient(*kubeConfigFile)
	} else {
		log.Infoln("Using in cluster config")
		k8sClient = k8s.NewInClusterClient()
	}

	// nodegroupConfigFile is required by kingpin. Won't get to here if it's not defined
	configFile, err := os.Open(*nodegroupConfigFile)
	if err != nil {
		log.Fatalf("Failed to open configFile: %v", err)
	}
	nodegroups, err := controller.UnmarshalNodeGroupOptions(configFile)
	if err != nil {
		log.Fatalf("Failed to decode configFile: %v", err)
	}

	// Validate each nodegroup options
	for _, nodegroup := range nodegroups {
		errs := controller.ValidateNodeGroup(nodegroup)
		if len(errs) > 0 {
			log.WithField("nodegroup", nodegroup.Name).Errorln("Validating options: [FAIL]")
			for _, err := range errs {
				log.WithError(err).Errorln("failed check")
			}
			log.WithField("nodegroup", nodegroup.Name).Fatalf("There are %v problems when validating the options. Please check %v", len(errs), *nodegroupConfigFile)
		}
		log.WithField("nodegroup", nodegroup.Name).Infoln("Validating options: [PASS]")
		log.WithField("nodegroup", nodegroup.Name).Infof("Registered with drymode %v", nodegroup.DryMode || *drymode)
	}

	opts := controller.Opts{
		ScanInterval: *scanInterval,
		K8SClient:    k8sClient,
		NodeGroups:   nodegroups,
		DryMode:      *drymode,
	}

	// signal channel waits for interrupt
	signalChan := make(chan os.Signal, 1)
	// global stop channel. Close signal will be sent to broadvast a shutdown to everything waiting for it to stop
	stopChan := make(chan struct{}, 1)

	// Handle termination signals and shutdown gracefully
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-signalChan
		log.Infof("Signal received: %v", sig)
		log.Infoln("Stopping autoscaler gracefully")
		close(stopChan)
	}()

	metrics.Start(*addr)

	c := controller.NewController(opts, stopChan)
	c.RunForever(true)
}
