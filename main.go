// Copyright (c) 2023, Ben Baker
// All rights reserved.
//
// This source code is licensed under the BSD-style license found in the
// LICENSE file in the root directory of this source tree.

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	toolsWatch "k8s.io/client-go/tools/watch"
)

var (
	watchExample = `
	# watch for pod events in all namespaces
	%[1]s watch pods

	# watch for the pod DELETED event in the default namespace
	%[1]s watch pods --namespace=default --event=DELETED

	# watch for the pod CHANGED event and when the pod status is Running
	%[1]s watch pods --event=CHANGED --status=Running

	# watch for the pod CHANGED event and when the pod status is Running
	%[1]s watch pod coredns-7cccd78cb7-p6mkn --event=CHANGED --status=Running
`

	errNoContext = fmt.Errorf("no context is currently set, use %q to select a new one", "kubectl config use-context <context>")
)

// WatchOptions provides information required to update
// the current context on a user's KUBECONFIG
type WatchOptions struct {
	configFlags *genericclioptions.ConfigFlags

	resultingContext     *api.Context
	resultingContextName string

	userSpecifiedCluster   string
	userSpecifiedContext   string
	userSpecifiedAuthInfo  string
	userSpecifiedNamespace string

	rawConfig    api.Config
	resource     string
	resourceName string
	eventType    string
	status       string
	args         []string

	genericiooptions.IOStreams
}

// NewWatchOptions provides an instance of WatchOptions with default values
func NewWatchOptions(streams genericiooptions.IOStreams) *WatchOptions {
	return &WatchOptions{
		configFlags: genericclioptions.NewConfigFlags(true),

		IOStreams: streams,
	}
}

// NewCmdNamespace provides a cobra command wrapping WatchOptions
func NewCmdNamespace(streams genericiooptions.IOStreams) *cobra.Command {
	o := NewWatchOptions(streams)

	cmd := &cobra.Command{
		Use:          "watch [TYPE] [NAME] [flags] [options]",
		Short:        "Watch for pod events in a namespace",
		Example:      fmt.Sprintf(watchExample, "kubectl"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := o.Complete(c, args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			if err := o.Run(); err != nil {
				return err
			}

			return nil
		},
	}

	cmd.Flags().StringVar(
		&o.eventType,
		"event",
		"",
		"Name of the event to watch. Options are 'ADDED', 'MODIFIED', 'DELETED' or 'BOOKMARK'",
	)

	cmd.Flags().StringVar(
		&o.status,
		"status",
		"",
		"Name of the status to watch. Options are 'Pending', 'Running', 'Succeeded' 'Failed' or 'Unknown'",
	)

	o.configFlags.AddFlags(cmd.Flags())

	return cmd
}

// Complete sets all information required for updating the current context
func (o *WatchOptions) Complete(cmd *cobra.Command, args []string) error {
	o.args = args

	var err error
	o.rawConfig, err = o.configFlags.ToRawKubeConfigLoader().RawConfig()
	if err != nil {
		return err
	}

	o.userSpecifiedNamespace, err = cmd.Flags().GetString("namespace")
	if err != nil {
		return err
	}

	if len(args) > 0 {
		o.resource = args[0]

		if (o.resource != "pod") && (o.resource != "pods") {
			return fmt.Errorf("Only pod resources are supported")
		}
	}

	if len(args) > 1 {
		o.resourceName = args[1]
	}

	// if no namespace argument or flag value was specified, then there
	// is no need to generate a resulting context
	if len(o.userSpecifiedNamespace) == 0 {
		return nil
	}

	o.userSpecifiedContext, err = cmd.Flags().GetString("context")
	if err != nil {
		return err
	}

	o.userSpecifiedCluster, err = cmd.Flags().GetString("cluster")
	if err != nil {
		return err
	}

	o.userSpecifiedAuthInfo, err = cmd.Flags().GetString("user")
	if err != nil {
		return err
	}

	currentContext, exists := o.rawConfig.Contexts[o.rawConfig.CurrentContext]
	if !exists {
		return errNoContext
	}

	o.resultingContext = api.NewContext()
	o.resultingContext.Cluster = currentContext.Cluster
	o.resultingContext.AuthInfo = currentContext.AuthInfo

	// if a target context is explicitly provided by the user,
	// use that as our reference for the final, resulting context
	if len(o.userSpecifiedContext) > 0 {
		o.resultingContextName = o.userSpecifiedContext
		if userCtx, exists := o.rawConfig.Contexts[o.userSpecifiedContext]; exists {
			o.resultingContext = userCtx.DeepCopy()
		}
	}

	// override context info with user provided values
	o.resultingContext.Namespace = o.userSpecifiedNamespace

	if len(o.userSpecifiedCluster) > 0 {
		o.resultingContext.Cluster = o.userSpecifiedCluster
	}
	if len(o.userSpecifiedAuthInfo) > 0 {
		o.resultingContext.AuthInfo = o.userSpecifiedAuthInfo
	}

	// generate a unique context name based on its new values if
	// user did not explicitly request a context by name
	if len(o.userSpecifiedContext) == 0 {
		o.resultingContextName = generateContextName(o.resultingContext)
	}

	return nil
}

func generateContextName(fromContext *api.Context) string {
	name := fromContext.Namespace
	if len(fromContext.Cluster) > 0 {
		name = fmt.Sprintf("%s/%s", name, fromContext.Cluster)
	}
	if len(fromContext.AuthInfo) > 0 {
		cleanAuthInfo := strings.Split(fromContext.AuthInfo, "/")[0]
		name = fmt.Sprintf("%s/%s", name, cleanAuthInfo)
	}

	return name
}

// Validate ensures that all required arguments and flag values are provided
func (o *WatchOptions) Validate() error {
	if len(o.rawConfig.CurrentContext) == 0 {
		return errNoContext
	}
	if len(o.args) > 2 {
		return fmt.Errorf("Too many arguments were provided. Expected 2, got %d", len(o.args))
	}

	return nil
}

// Run lists all available namespaces on a user's KUBECONFIG or updates the
// current context based on a provided namespace.
func (o *WatchOptions) Run() error {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	config, _ := kubeConfig.ClientConfig()
	clientset, _ := kubernetes.NewForConfig(config)

	var wg sync.WaitGroup
	go watchPods(clientset, o)
	wg.Add(1)
	wg.Wait()

	return nil
}

func getPods(clientset *kubernetes.Clientset, namespace string) (*corev1.PodList, error) {
	// Create a pod interface for the given namespace
	podInterface := clientset.CoreV1().Pods(namespace)

	// Timeout after 60 seconds
	timeOut := int64(60)

	// List the pods in the given namespace
	podList, err := podInterface.List(context.Background(), metav1.ListOptions{TimeoutSeconds: &timeOut})

	if err != nil {
		return nil, err
	}

	return podList, nil
}

func watchPods(clientset *kubernetes.Clientset, o *WatchOptions) {
	namespace := "default"

	if len(o.userSpecifiedNamespace) > 0 && o.resultingContext != nil {
		namespace = o.userSpecifiedNamespace
	} else {
		for name, c := range o.rawConfig.Contexts {
			if name != o.rawConfig.CurrentContext {
				continue
			}

			namespace = c.Namespace

			break
		}
	}

	fmt.Printf("%-10s %-40s %-10s %-10s %-12s %-10s\n", "EVENT", "NAME", "READY", "STATUS", "RESTARTS", "AGE")

	watchFunc := func(options metav1.ListOptions) (watch.Interface, error) {
		timeOut := int64(60)
		return clientset.CoreV1().Pods(namespace).Watch(context.Background(), metav1.ListOptions{TimeoutSeconds: &timeOut})
	}

	watcher, _ := toolsWatch.NewRetryWatcher("1", &cache.ListWatch{WatchFunc: watchFunc})

	for event := range watcher.ResultChan() {
		eventType := string(event.Type)

		if o.eventType != "" && eventType != o.eventType {
			continue
		}

		pod := event.Object.(*corev1.Pod)
		podStatus := pod.Status

		if o.status != "" && string(podStatus.Phase) != o.status {
			continue
		}

		name := pod.GetName()

		if o.resourceName != "" && name != o.resourceName {
			continue
		}

		podCreationTime := pod.GetCreationTimestamp()
		age := time.Since(podCreationTime.Time).Round(time.Second)

		var containerRestarts int32
		var containerReady int
		var totalContainers int

		for container := range pod.Spec.Containers {
			if len(podStatus.ContainerStatuses) > 0 {
				containerRestarts += podStatus.ContainerStatuses[container].RestartCount
				if podStatus.ContainerStatuses[container].Ready {
					containerReady++
				}
			}
			totalContainers++
		}

		ready := fmt.Sprintf("%v/%v", containerReady, totalContainers)
		status := fmt.Sprintf("%v", podStatus.Phase)
		restarts := fmt.Sprintf("%v", containerRestarts)
		ageS := age.String()

		fmt.Printf("%-10s %-40s %-10s %-10s %-12s %-10s\n", eventType, name, ready, status, restarts, ageS)
	}
}

func main() {
	flags := pflag.NewFlagSet("kubectl-watch", pflag.ExitOnError)
	pflag.CommandLine = flags

	root := NewCmdNamespace(genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
