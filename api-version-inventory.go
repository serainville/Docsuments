package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"log"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"
const declaredVersionLabel = "configsync.gke.io/declared-version"

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("usage: %s <resource-or-kind>.<api-group>", os.Args[0])
	}

	target, group, found := strings.Cut(os.Args[1], ".")
	if !found {
		log.Fatal("argument must be in the form <resource-or-kind>.<api-group>")
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		log.Fatal(err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	groupList, err := discoveryClient.ServerGroups()
	if err != nil {
		log.Fatal(err)
	}

	var preferredGroupVersion string
	for _, apiGroup := range groupList.Groups {
		if apiGroup.Name == group {
			preferredGroupVersion = apiGroup.PreferredVersion.GroupVersion
			break
		}
	}

	if preferredGroupVersion == "" {
		log.Fatalf("API group %q not found", group)
	}

	resourceList, err := discoveryClient.ServerResourcesForGroupVersion(
		preferredGroupVersion,
	)
	if err != nil {
		log.Fatal(err)
	}

	var apiResource *metav1.APIResource
	for i := range resourceList.APIResources {
		resource := &resourceList.APIResources[i]

		if resource.Name == target ||
			strings.EqualFold(resource.Kind, target) {
			apiResource = resource
			break
		}
	}

	if apiResource == nil {
		log.Fatalf(
			"resource or kind %q not found in %s",
			target,
			preferredGroupVersion,
		)
	}

	if !hasVerb(apiResource.Verbs, "list") {
		log.Fatalf("resource %q does not support list", apiResource.Name)
	}

	groupVersion, err := schema.ParseGroupVersion(preferredGroupVersion)
	if err != nil {
		log.Fatal(err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	resourceClient := dynamicClient.Resource(
		groupVersion.WithResource(apiResource.Name),
	)

	var listErr error
	var items []map[string]any

	if apiResource.Namespaced {
		list, err := resourceClient.
			Namespace(metav1.NamespaceAll).
			List(context.Background(), metav1.ListOptions{})
		listErr = err

		if list != nil {
			for _, item := range list.Items {
				items = append(items, item.Object)
			}
		}
	} else {
		list, err := resourceClient.
			List(context.Background(), metav1.ListOptions{})
		listErr = err

		if list != nil {
			for _, item := range list.Items {
				items = append(items, item.Object)
			}
		}
	}

	if listErr != nil {
		log.Fatal(listErr)
	}

	writer := csv.NewWriter(os.Stdout)

	_ = writer.Write([]string{
		"apiVersion",
		"lastAppliedApiVersion",
		"declaredVersion",
		"namespace",
		"name",
	})

	for _, object := range items {
		metadata, _ := object["metadata"].(map[string]any)
		annotations, _ := metadata["annotations"].(map[string]any)
		labels, _ := metadata["labels"].(map[string]any)

		_ = writer.Write([]string{
			stringValue(object["apiVersion"]),
			lastAppliedAPIVersion(stringValue(annotations[lastAppliedAnnotation])),
			stringValue(labels[declaredVersionLabel]),
			stringValue(metadata["namespace"]),
			stringValue(metadata["name"]),
		})
	}

	writer.Flush()

	if err := writer.Error(); err != nil {
		log.Fatal(err)
	}
}

func lastAppliedAPIVersion(value string) string {
	if value == "" {
		return ""
	}

	var manifest struct {
		APIVersion string `json:"apiVersion"`
	}

	if err := json.Unmarshal([]byte(value), &manifest); err != nil {
		return ""
	}

	return manifest.APIVersion
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func hasVerb(verbs []string, target string) bool {
	for _, verb := range verbs {
		if verb == target {
			return true
		}
	}

	return false
}
