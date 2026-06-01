/*
Copyright 2017, 2019, 2020 the Velero contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package backup

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1api "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	velerov1api "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"github.com/vmware-tanzu/velero/pkg/builder"
	"github.com/vmware-tanzu/velero/pkg/kuberesource"
	"github.com/vmware-tanzu/velero/pkg/test"
	"github.com/vmware-tanzu/velero/pkg/util/collections"
)

func TestSortCoreGroup(t *testing.T) {
	group := &metav1.APIResourceList{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "persistentvolumes"},
			{Name: "configmaps"},
			{Name: "antelopes"},
			{Name: "persistentvolumeclaims"},
			{Name: "pods"},
		},
	}

	sortCoreGroup(group)

	expected := []string{
		"pods",
		"persistentvolumeclaims",
		"persistentvolumes",
		"configmaps",
		"antelopes",
	}
	for i, r := range group.APIResources {
		assert.Equal(t, expected[i], r.Name)
	}
}

func TestSortOrderedResource(t *testing.T) {
	log := logrus.StandardLogger()
	podResources := []*kubernetesResource{
		{namespace: "ns1", name: "pod3"},
		{namespace: "ns1", name: "pod1"},
		{namespace: "ns1", name: "pod2"},
	}
	order := []string{"ns1/pod2", "ns1/pod1"}
	expectedResources := []*kubernetesResource{
		{namespace: "ns1", name: "pod2", orderedResource: true},
		{namespace: "ns1", name: "pod1", orderedResource: true},
		{namespace: "ns1", name: "pod3"},
	}
	sortedResources := sortResourcesByOrder(log, podResources, order)
	assert.Equal(t, expectedResources, sortedResources)

	// Test cluster resources
	pvResources := []*kubernetesResource{
		{name: "pv1"},
		{name: "pv2"},
		{name: "pv3"},
	}
	pvOrder := []string{"pv5", "pv2", "pv1"}
	expectedPvResources := []*kubernetesResource{
		{name: "pv2", orderedResource: true},
		{name: "pv1", orderedResource: true},
		{name: "pv3"},
	}
	sortedPvResources := sortResourcesByOrder(log, pvResources, pvOrder)
	assert.Equal(t, expectedPvResources, sortedPvResources)
}

func TestFilterNamespaces(t *testing.T) {
	tests := []struct {
		name              string
		resources         []*kubernetesResource
		needToTrack       string
		expectedResources []*kubernetesResource
	}{
		{
			name: "Namespace include by the filter but not in namespacesContainResource",
			resources: []*kubernetesResource{
				{
					groupResource: kuberesource.Namespaces,
					preferredGVR:  kuberesource.Namespaces.WithVersion("v1"),
					name:          "ns1",
				},
				{
					groupResource: kuberesource.Namespaces,
					preferredGVR:  kuberesource.Namespaces.WithVersion("v1"),
					name:          "ns2",
				},
				{
					groupResource: kuberesource.Pods,
					preferredGVR:  kuberesource.Namespaces.WithVersion("v1"),
					name:          "pod1",
				},
			},
			needToTrack: "ns1",
			expectedResources: []*kubernetesResource{
				{
					groupResource: kuberesource.Namespaces,
					preferredGVR:  kuberesource.Namespaces.WithVersion("v1"),
					name:          "ns1",
				},
				{
					groupResource: kuberesource.Pods,
					preferredGVR:  kuberesource.Namespaces.WithVersion("v1"),
					name:          "pod1",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(*testing.T) {
			r := itemCollector{
				backupRequest: &Request{},
			}

			if tc.needToTrack != "" {
				r.nsTracker.track(tc.needToTrack)
			}

			require.Equal(t, tc.expectedResources, r.nsTracker.filterNamespaces(tc.resources))
		})
	}
}

func TestItemCollectorBackupNamespaces(t *testing.T) {
	tests := []struct {
		name              string
		ie                *collections.NamespaceIncludesExcludes
		namespaces        []*corev1api.Namespace
		backup            *velerov1api.Backup
		expectedTrackedNS []string
		converter         runtime.UnstructuredConverter
	}{
		{
			name:   "ns filter by namespace IE filter",
			backup: builder.ForBackup("velero", "backup").Result(),
			ie:     collections.NewNamespaceIncludesExcludes().Includes("ns1"),
			namespaces: []*corev1api.Namespace{
				builder.ForNamespace("ns1").Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns2").Phase(corev1api.NamespaceActive).Result(),
			},
			expectedTrackedNS: []string{"ns1"},
		},
		{
			name: "ns filter by backup labelSelector",
			backup: builder.ForBackup("velero", "backup").LabelSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{"name": "ns1"},
			}).Result(),
			ie: collections.NewNamespaceIncludesExcludes().Includes("*"),
			namespaces: []*corev1api.Namespace{
				builder.ForNamespace("ns1").ObjectMeta(builder.WithLabels("name", "ns1")).Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns2").Phase(corev1api.NamespaceActive).Result(),
			},
			expectedTrackedNS: []string{"ns1"},
		},
		{
			name: "ns filter by backup orLabelSelector",
			backup: builder.ForBackup("velero", "backup").OrLabelSelector([]*metav1.LabelSelector{
				{MatchLabels: map[string]string{"name": "ns1"}},
			}).Result(),
			ie: collections.NewNamespaceIncludesExcludes().Includes("*"),
			namespaces: []*corev1api.Namespace{
				builder.ForNamespace("ns1").ObjectMeta(builder.WithLabels("name", "ns1")).Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns2").Phase(corev1api.NamespaceActive).Result(),
			},
			expectedTrackedNS: []string{"ns1"},
		},
		{
			name: "ns not included by IE filter, but included by labelSelector",
			backup: builder.ForBackup("velero", "backup").LabelSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{"name": "ns1"},
			}).Result(),
			ie: collections.NewNamespaceIncludesExcludes().Excludes("ns1"),
			namespaces: []*corev1api.Namespace{
				builder.ForNamespace("ns1").ObjectMeta(builder.WithLabels("name", "ns1")).Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns2").Phase(corev1api.NamespaceActive).Result(),
			},
			expectedTrackedNS: []string{"ns1"},
		},
		{
			name: "ns not included by IE filter, but included by orLabelSelector",
			backup: builder.ForBackup("velero", "backup").OrLabelSelector([]*metav1.LabelSelector{
				{MatchLabels: map[string]string{"name": "ns1"}},
			}).Result(),
			ie: collections.NewNamespaceIncludesExcludes().Excludes("ns1", "ns2"),
			namespaces: []*corev1api.Namespace{
				builder.ForNamespace("ns1").ObjectMeta(builder.WithLabels("name", "ns1")).Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns2").Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns3").Phase(corev1api.NamespaceActive).Result(),
			},
			expectedTrackedNS: []string{"ns1", "ns3"},
		},
		{
			name:   "No ns filters",
			backup: builder.ForBackup("velero", "backup").Result(),
			ie:     collections.NewNamespaceIncludesExcludes().Includes("*"),
			namespaces: []*corev1api.Namespace{
				builder.ForNamespace("ns1").ObjectMeta(builder.WithLabels("name", "ns1")).Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns2").Phase(corev1api.NamespaceActive).Result(),
			},
			expectedTrackedNS: []string{"ns1", "ns2"},
		},
		{
			name:   "ns specified by the IncludeNamespaces cannot be found",
			backup: builder.ForBackup("velero", "backup").IncludedNamespaces("ns1", "invalid", "*").Result(),
			ie:     collections.NewNamespaceIncludesExcludes().Includes("ns1", "invalid", "*"),
			namespaces: []*corev1api.Namespace{
				builder.ForNamespace("ns1").ObjectMeta(builder.WithLabels("name", "ns1")).Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns2").Phase(corev1api.NamespaceActive).Result(),
				builder.ForNamespace("ns3").Phase(corev1api.NamespaceActive).Result(),
			},
			expectedTrackedNS: []string{"ns1"},
		},
		{
			name:   "terminating ns should not tracked",
			backup: builder.ForBackup("velero", "backup").Result(),
			ie:     collections.NewNamespaceIncludesExcludes().Includes("ns1", "ns2"),
			namespaces: []*corev1api.Namespace{
				builder.ForNamespace("ns1").Phase(corev1api.NamespaceTerminating).Result(),
				builder.ForNamespace("ns2").Phase(corev1api.NamespaceActive).Result(),
			},
			expectedTrackedNS: []string{"ns2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(*testing.T) {
			tempDir := t.TempDir()

			var unstructuredNSList unstructured.UnstructuredList
			for _, ns := range tc.namespaces {
				unstructuredNS, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ns)
				require.NoError(t, err)
				unstructuredNSList.Items = append(unstructuredNSList.Items,
					unstructured.Unstructured{Object: unstructuredNS})
			}

			dc := &test.FakeDynamicClient{}
			dc.On("List", mock.Anything).Return(&unstructuredNSList, nil)

			factory := &test.FakeDynamicFactory{}
			factory.On(
				"ClientForGroupVersionResource",
				mock.Anything,
				mock.Anything,
				mock.Anything,
			).Return(dc, nil)

			r := itemCollector{
				backupRequest: &Request{
					Backup:                    tc.backup,
					NamespaceIncludesExcludes: tc.ie,
				},
				dynamicFactory: factory,
				dir:            tempDir,
			}

			if tc.converter == nil {
				tc.converter = runtime.DefaultUnstructuredConverter
			}

			r.collectNamespaces(
				metav1.APIResource{
					Name:       "Namespace",
					Kind:       "Namespace",
					Namespaced: false,
				},
				kuberesource.Namespaces.WithVersion("").GroupVersion(),
				kuberesource.Namespaces,
				kuberesource.Namespaces.WithVersion(""),
				logrus.StandardLogger(),
			)

			for _, ns := range tc.expectedTrackedNS {
				require.True(t, r.nsTracker.isTracked(ns))
			}
		})
	}
}

// fakeResourceIE implements collections.IncludesExcludesInterface
// for test purposes.
type fakeResourceIE struct {
	shouldInclude bool
}

func (f *fakeResourceIE) ShouldInclude(_ string) bool { return f.shouldInclude }
func (f *fakeResourceIE) ShouldExclude(_ string) bool { return !f.shouldInclude }

func TestClusterWideLISTOptimization(t *testing.T) {
	tests := []struct {
		name string
		// backup spec
		labelSelector    *metav1.LabelSelector
		orLabelSelectors []*metav1.LabelSelector
		// namespace IE setup
		nsIncludes []string
		nsExcludes []string
		// items returned by the cluster-wide list
		returnedItems []unstructured.Unstructured
		// whether we expect a single cluster-wide call (namespace="")
		// vs per-namespace calls
		expectClusterWideLIST bool
		// expected number of ClientForGroupVersionResource calls
		expectedClientCalls int
	}{
		{
			name: "labelSelector with all namespaces uses cluster-wide LIST",
			labelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"backup": "true"},
			},
			nsIncludes: []string{"*"},
			returnedItems: []unstructured.Unstructured{
				*newUnstructuredWithNamespace("v1", "ConfigMap", "ns1", "cm1"),
				*newUnstructuredWithNamespace("v1", "ConfigMap", "ns2", "cm2"),
			},
			expectClusterWideLIST: true,
			expectedClientCalls:   1,
		},
		{
			name: "orLabelSelectors with all namespaces uses cluster-wide LIST",
			orLabelSelectors: []*metav1.LabelSelector{
				{MatchLabels: map[string]string{"backup": "true"}},
			},
			nsIncludes: []string{"*"},
			returnedItems: []unstructured.Unstructured{
				*newUnstructuredWithNamespace("v1", "ConfigMap", "ns1", "cm1"),
			},
			expectClusterWideLIST: true,
			expectedClientCalls:   1,
		},
		{
			name: "labelSelector with specific namespaces does NOT use cluster-wide LIST",
			labelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"backup": "true"},
			},
			nsIncludes:            []string{"ns1", "ns2"},
			expectClusterWideLIST: false,
			expectedClientCalls:   2,
		},
		{
			name: "labelSelector with excluded namespace does NOT use cluster-wide LIST",
			labelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"backup": "true"},
			},
			nsIncludes:            []string{"*"},
			nsExcludes:            []string{"kube-system"},
			expectClusterWideLIST: false,
			expectedClientCalls:   0, // not checked, just needs >1
		},
		{
			name:                  "no labelSelector does NOT use cluster-wide LIST",
			nsIncludes:            []string{"*"},
			expectClusterWideLIST: false,
			expectedClientCalls:   0, // not checked
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()

			// Build the backup
			backupBuilder := builder.ForBackup("velero", "backup")
			if tc.labelSelector != nil {
				backupBuilder.LabelSelector(tc.labelSelector)
			}
			if tc.orLabelSelectors != nil {
				backupBuilder.OrLabelSelector(tc.orLabelSelectors)
			}
			backup := backupBuilder.Result()

			// Build namespace IE
			ie := collections.NewNamespaceIncludesExcludes()
			if len(tc.nsIncludes) > 0 {
				ie.Includes(tc.nsIncludes...)
			}
			if len(tc.nsExcludes) > 0 {
				ie.Excludes(tc.nsExcludes...)
			}

			// Set up dynamic client mock that returns items
			returnList := &unstructured.UnstructuredList{Items: tc.returnedItems}

			dc := &test.FakeDynamicClient{}
			dc.On("List", mock.Anything).Return(returnList, nil)

			factory := &test.FakeDynamicFactory{}
			factory.On(
				"ClientForGroupVersionResource",
				mock.Anything,
				mock.Anything,
				mock.Anything,
			).Return(dc, nil)

			discoveryHelper := &test.FakeDiscoveryHelper{
				AutoReturnResource: true,
			}

			r := itemCollector{
				log: logrus.StandardLogger(),
				backupRequest: &Request{
					Backup:                    backup,
					NamespaceIncludesExcludes: ie,
					ResourceIncludesExcludes:  &fakeResourceIE{shouldInclude: true},
				},
				discoveryHelper: discoveryHelper,
				dynamicFactory:  factory,
				dir:             tempDir,
			}

			gv := schema.GroupVersion{Group: "", Version: "v1"}
			resource := metav1.APIResource{
				Name:       "configmaps",
				Kind:       "ConfigMap",
				Namespaced: true,
			}

			items, err := r.getResourceItems(
				logrus.StandardLogger(), gv, resource, nil,
			)
			require.NoError(t, err)

			if tc.expectClusterWideLIST {
				// Verify ClientForGroupVersionResource was called with namespace=""
				var clusterWideCallFound bool
				for _, call := range factory.Calls {
					if call.Method == "ClientForGroupVersionResource" {
						ns := call.Arguments.Get(2).(string)
						if ns == "" {
							clusterWideCallFound = true
						}
					}
				}
				assert.True(t, clusterWideCallFound,
					"expected cluster-wide LIST (namespace='') but it was not called")

				// Verify correct number of items returned
				assert.Len(t, items, len(tc.returnedItems))

				// Verify nsTracker picked up namespaces from returned items
				for _, item := range tc.returnedItems {
					if item.GetNamespace() != "" {
						assert.True(t, r.nsTracker.isTracked(item.GetNamespace()),
							"expected namespace %s to be tracked", item.GetNamespace())
					}
				}
			}

			if !tc.expectClusterWideLIST && tc.expectedClientCalls > 0 {
				// Verify we did NOT get a cluster-wide call
				for _, call := range factory.Calls {
					if call.Method == "ClientForGroupVersionResource" {
						ns := call.Arguments.Get(2).(string)
						assert.NotEmpty(t, ns,
							"expected per-namespace LIST but got cluster-wide call")
					}
				}
			}
		})
	}
}

func newUnstructuredWithNamespace(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata": map[string]interface{}{
				"namespace": namespace,
				"name":      name,
			},
		},
	}
}
