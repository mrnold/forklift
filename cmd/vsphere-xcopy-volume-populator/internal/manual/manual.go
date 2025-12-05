package manual

import (
	"context"

	"github.com/kubev2v/forklift/cmd/vsphere-xcopy-volume-populator/internal/populator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type ManualCloner struct {
	clientset *kubernetes.Clientset
	namespace string
}

func NewManualCloner(clientset *kubernetes.Clientset, namespace string) (ManualCloner, error) {
	return ManualCloner{clientset: clientset, namespace: namespace}, nil
}

func (m *ManualCloner) EnsureClonnerIgroup(initiatorGroup string, clonnerIqn []string) (populator.MappingContext, error) {
	klog.Infof("TEST: EnsureClonnerIgroup: %s, %v", initiatorGroup, clonnerIqn)
	return populator.MappingContext{}, nil
}

func (m *ManualCloner) Map(initatorGroup string, targetLUN populator.LUN, context populator.MappingContext) (populator.LUN, error) {
	klog.Infof("TEST: Map: %s, %v, %v", initatorGroup, targetLUN, context)
	mappedLUN := targetLUN
	return mappedLUN, nil
}

func (m *ManualCloner) UnMap(initatorGroup string, targetLUN populator.LUN, context populator.MappingContext) error {
	klog.Infof("TEST: UnMap: %s, %v, %v", initatorGroup, targetLUN, context)
	return nil
}

func (m *ManualCloner) CurrentMappedGroups(targetLUN populator.LUN, context populator.MappingContext) ([]string, error) {
	klog.Infof("TEST: CurrentMappedGroups: %v, %v", targetLUN, context)
	return []string{}, nil
}

func (m *ManualCloner) ResolvePVToLUN(pv populator.PersistentVolume) (populator.LUN, error) {
	klog.Infof("TEST: ResolvePVToLUN: %v", pv)
	// ConfigMap should look like:
	// apiVersion: v1
	// kind: ConfigMap
	// metadata:
	//   name: manual-lun-map
	// data:
	//   pvc-mpathc: naa.6001405d00c662d05914674b13b7ae5e
	//   pvc-mpathd: naa.6001405757ed03cc6364cc59ed8d5a4e
	//   pvc-mpathe: naa.60014053d021fde195c4aa48008e50fb
	lunmap, err := m.clientset.CoreV1().ConfigMaps(m.namespace).Get(context.Background(), "manual-lun-map", metav1.GetOptions{})
	if err != nil {
		return populator.LUN{}, err
	}
	lun := populator.LUN{
		Name: lunmap.Data[pv.Name],
		NAA:  lunmap.Data[pv.Name],
	}
	return lun, nil
}
