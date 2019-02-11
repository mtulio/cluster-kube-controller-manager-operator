package operator

import (
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	operatorv1client "github.com/openshift/client-go/operator/clientset/versioned"
	operatorv1informers "github.com/openshift/client-go/operator/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/staticpod"
	"github.com/openshift/library-go/pkg/operator/staticpod/controller/revision"
	"github.com/openshift/library-go/pkg/operator/status"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"github.com/openshift/cluster-kube-controller-manager-operator/pkg/operator/certrotationcontroller"
	"github.com/openshift/cluster-kube-controller-manager-operator/pkg/operator/configobservation/configobservercontroller"
	"github.com/openshift/cluster-kube-controller-manager-operator/pkg/operator/operatorclient"
	"github.com/openshift/cluster-kube-controller-manager-operator/pkg/operator/resourcesynccontroller"
	"github.com/openshift/cluster-kube-controller-manager-operator/pkg/operator/targetconfigcontroller"
	"github.com/openshift/cluster-kube-controller-manager-operator/pkg/operator/v311_00_assets"
)

func RunOperator(ctx *controllercmd.ControllerContext) error {
	// This kube client use protobuf, do not use it for CR
	kubeClient, err := kubernetes.NewForConfig(ctx.ProtoKubeConfig)
	if err != nil {
		return err
	}
	operatorConfigClient, err := operatorv1client.NewForConfig(ctx.KubeConfig)
	if err != nil {
		return err
	}
	dynamicClient, err := dynamic.NewForConfig(ctx.KubeConfig)
	if err != nil {
		return err
	}
	configClient, err := configv1client.NewForConfig(ctx.KubeConfig)
	if err != nil {
		return err
	}

	operatorConfigInformers := operatorv1informers.NewSharedInformerFactory(operatorConfigClient, 10*time.Minute)
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient,
		"",
		operatorclient.GlobalUserSpecifiedConfigNamespace,
		operatorclient.GlobalMachineSpecifiedConfigNamespace,
		operatorclient.OperatorNamespace,
		operatorclient.TargetNamespace,
		"kube-system",
	)
	operatorClient := &operatorclient.OperatorClient{
		Informers: operatorConfigInformers,
		Client:    operatorConfigClient.OperatorV1(),
	}

	v1helpers.EnsureOperatorConfigExists(
		dynamicClient,
		v311_00_assets.MustAsset("v3.11.0/kube-controller-manager/operator-config.yaml"),
		schema.GroupVersionResource{Group: operatorv1.GroupName, Version: operatorv1.GroupVersion.Version, Resource: "kubecontrollermanagers"},
	)

	resourceSyncController, err := resourcesynccontroller.NewResourceSyncController(
		operatorClient,
		kubeInformersForNamespaces,
		v1helpers.CachedSecretGetter(kubeClient.CoreV1(), kubeInformersForNamespaces),
		v1helpers.CachedConfigMapGetter(kubeClient.CoreV1(), kubeInformersForNamespaces),
		ctx.EventRecorder,
	)
	if err != nil {
		return err
	}
	configObserver := configobservercontroller.NewConfigObserver(
		operatorClient,
		operatorConfigInformers,
		kubeInformersForNamespaces.InformersFor("kube-system"),
		resourceSyncController,
		ctx.EventRecorder,
	)
	targetConfigController := targetconfigcontroller.NewTargetConfigController(
		os.Getenv("IMAGE"),
		kubeInformersForNamespaces,
		operatorConfigInformers.Operator().V1().KubeControllerManagers(),
		kubeInformersForNamespaces.InformersFor(operatorclient.TargetNamespace),
		operatorConfigClient.OperatorV1(),
		operatorClient,
		kubeClient,
		ctx.EventRecorder,
	)

	staticPodControllers := staticpod.NewControllers(
		operatorclient.TargetNamespace,
		"openshift-kube-controller-manager",
		"kube-controller-manager-pod",
		[]string{"cluster-kube-controller-manager-operator", "installer"},
		[]string{"cluster-kube-controller-manager-operator", "prune"},
		deploymentConfigMaps,
		deploymentSecrets,
		operatorClient,
		v1helpers.CachedConfigMapGetter(kubeClient.CoreV1(), kubeInformersForNamespaces),
		v1helpers.CachedSecretGetter(kubeClient.CoreV1(), kubeInformersForNamespaces),
		kubeClient.CoreV1(),
		kubeClient,
		dynamicClient,
		kubeInformersForNamespaces.InformersFor(operatorclient.TargetNamespace),
		kubeInformersForNamespaces.InformersFor(""),
		ctx.EventRecorder,
	)

	clusterOperatorStatus := status.NewClusterOperatorStatusController(
		"kube-controller-manager",
		[]configv1.ObjectReference{
			{Group: "operator.openshift.io", Resource: "kubecontrollermanagers", Name: "cluster"},
			{Resource: "namespaces", Name: "openshift-config"},
			{Resource: "namespaces", Name: "openshift-config-managed"},
			{Resource: "namespaces", Name: operatorclient.TargetNamespace},
			{Resource: "namespaces", Name: "openshift-kube-controller-manager-operator"},
		},
		configClient.ConfigV1(),
		operatorClient,
		status.NewVersionGetter(),
		ctx.EventRecorder,
	)

	certRotationController, err := certrotationcontroller.NewCertRotationController(
		v1helpers.CachedSecretGetter(kubeClient.CoreV1(), kubeInformersForNamespaces),
		v1helpers.CachedConfigMapGetter(kubeClient.CoreV1(), kubeInformersForNamespaces),
		operatorClient,
		kubeInformersForNamespaces,
		ctx.EventRecorder,
	)
	if err != nil {
		return err
	}
	saTokenController, err := certrotationcontroller.NewSATokenSignerController(operatorClient, kubeInformersForNamespaces, kubeClient, ctx.EventRecorder)
	if err != nil {
		return err
	}

	operatorConfigInformers.Start(ctx.Done())
	kubeInformersForNamespaces.Start(ctx.Done())

	go staticPodControllers.Run(ctx.Done())
	go targetConfigController.Run(1, ctx.Done())
	go configObserver.Run(1, ctx.Done())
	go clusterOperatorStatus.Run(1, ctx.Done())
	go resourceSyncController.Run(1, ctx.Done())
	go certRotationController.Run(1, ctx.Done())
	go saTokenController.Run(1, ctx.Done())

	<-ctx.Done()
	return fmt.Errorf("stopped")
}

// deploymentConfigMaps is a list of configmaps that are directly copied for the current values.  A different actor/controller modifies these.
// the first element should be the configmap that contains the static pod manifest
var deploymentConfigMaps = []revision.RevisionResource{
	{Name: "kube-controller-manager-pod"},

	{Name: "config"},
	{Name: "controller-manager-kubeconfig"},
	{Name: "serviceaccount-ca"},
}

// deploymentSecrets is a list of secrets that are directly copied for the current values.  A different actor/controller modifies these.
var deploymentSecrets = []revision.RevisionResource{
	{Name: "csr-signer"},
	{Name: "kube-controller-manager-client-cert-key"},
	{Name: "service-account-private-key"},

	// this cert is created by the service-ca controller, which doesn't come up until after we are available. this piece of config must be optional.
	{Name: "serving-cert", Optional: true},
}
