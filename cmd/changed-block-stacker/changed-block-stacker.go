package main

import (
	"context"
	"flag"

	forkliftapis "github.com/kubev2v/forklift/pkg/apis"
	forkliftv1beta1 "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	var endpoint, cr, namespace string
	flag.StringVar(&endpoint, "endpoint", "", "")
	flag.StringVar(&cr, "cr", "", "")
	flag.StringVar(&namespace, "namespace", "", "")
	flag.Parse()

	klog.Info("Changed block stacker")

	cfg, err := config.GetConfig()
	if err != nil {
		klog.Fatalf("failed to get kubernetes config: %v", err)
	}

	scheme := runtime.NewScheme()
	forkliftapis.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		klog.Fatalf("failed to create kubernetes client: %v", err)
	}

	stacker := &forkliftv1beta1.ChangedBlockStacker{}
	c.Get(context.TODO(), client.ObjectKey{Namespace: namespace, Name: cr}, stacker)
	if stacker.Spec.Nutanix != nil {

	} else if stacker.Spec.VMware != nil {

	}
}
