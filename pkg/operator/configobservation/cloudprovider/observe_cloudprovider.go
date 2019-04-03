package cloudprovider

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"

	"github.com/openshift/cluster-kube-controller-manager-operator/pkg/operator/configobservation"
)

const (
	targetNamespaceName       = "openshift-kube-controller-manager"
	cloudProviderConfFilePath = "/etc/kubernetes/static-pod-resources/configmaps/cloud-config/config"
	configNamespace           = "openshift-config"
)

// ObserveCloudProviderNames observes the cloud provider from the global cluster infrastructure resource.
func ObserveCloudProviderNames(genericListers configobserver.Listers, recorder events.Recorder, existingConfig map[string]interface{}) (map[string]interface{}, []error) {
	listers := genericListers.(configobservation.Listers)
	var errs []error
	cloudProvidersPath := []string{"extendedArguments", "cloud-provider"}
	cloudProviderConfPath := []string{"extendedArguments", "cloud-config"}

	previouslyObservedConfig := map[string]interface{}{}
	if currentCloudProvider, _, _ := unstructured.NestedStringSlice(existingConfig, cloudProvidersPath...); len(currentCloudProvider) > 0 {
		if err := unstructured.SetNestedStringSlice(previouslyObservedConfig, currentCloudProvider, cloudProvidersPath...); err != nil {
			errs = append(errs, err)
		}
	}

	if currentCloudConfig, _, _ := unstructured.NestedStringSlice(existingConfig, cloudProviderConfPath...); len(currentCloudConfig) > 0 {
		if err := unstructured.SetNestedStringSlice(previouslyObservedConfig, currentCloudConfig, cloudProviderConfPath...); err != nil {
			errs = append(errs, err)
		}
	}

	observedConfig := map[string]interface{}{}

	infrastructure, err := listers.InfrastructureLister.Get("cluster")
	if errors.IsNotFound(err) {
		recorder.Warningf("ObserverCloudProviderNames", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return observedConfig, errs
	}
	if err != nil {
		return previouslyObservedConfig, errs
	}

	cloudProvider := getPlatformName(infrastructure.Status.Platform, recorder)
	if len(cloudProvider) > 0 {
		if err := unstructured.SetNestedStringSlice(observedConfig, []string{cloudProvider}, cloudProvidersPath...); err != nil {
			errs = append(errs, err)
		}
	}

	sourceCloudConfigMap := infrastructure.Spec.CloudConfig.Name
	sourceCloudConfigNamespace := configNamespace
	if len(sourceCloudConfigMap) == 0 {
		return observedConfig, errs
	}

	err = listers.ResourceSyncer().SyncConfigMap(
		resourcesynccontroller.ResourceLocation{
			Namespace: targetNamespaceName,
			Name:      "cloud-config",
		},
		resourcesynccontroller.ResourceLocation{
			Namespace: sourceCloudConfigNamespace,
			Name:      sourceCloudConfigMap,
		},
	)
	if err != nil {
		errs = append(errs, err)
		return observedConfig, errs
	}
	if err := unstructured.SetNestedStringSlice(observedConfig, []string{cloudProviderConfFilePath}, cloudProviderConfPath...); err != nil {
		recorder.Warningf("ObserverCloudProviderNames", "Failed setting cloud-config : %v", err)
		errs = append(errs, err)
	}
	return observedConfig, errs
}

func getPlatformName(platformType configv1.PlatformType, recorder events.Recorder) string {
	cloudProvider := ""
	switch platformType {
	case "":
		recorder.Warningf("ObserveCloudProvidersFailed", "Required status.platform field is not set in infrastructures.%s/cluster", configv1.GroupName)
	case configv1.AWSPlatformType:
		cloudProvider = "aws"
	case configv1.AzurePlatformType:
		cloudProvider = "azure"
	case configv1.VSpherePlatformType:
		cloudProvider = "vsphere"
	case configv1.LibvirtPlatformType:
	case configv1.OpenStackPlatformType:
		// TODO(flaper87): Enable this once we've figured out a way to write the cloud provider config in the master nodes
		//cloudProvider = "openstack"
	case configv1.NonePlatformType:
	default:
		// the new doc on the infrastructure fields requires that we treat an unrecognized thing the same bare metal.
		// TODO find a way to indicate to the user that we didn't honor their choice
		recorder.Warningf("ObserveCloudProvidersFailed", fmt.Sprintf("No recognized cloud provider platform found in infrastructures.%s/cluster.status.platform", configv1.GroupName))
	}
	return cloudProvider
}
