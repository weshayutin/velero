/*
Copyright The Velero Contributors.

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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	velerov1api "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"fmt"
	"sort"
	// "strings" // Removed unused import for strings package

	"github.com/spf13/cobra"
	v1core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	fakekube "k8s.io/client-go/kubernetes/fake"
	kbclient "sigs.k8s.io/controller-runtime/pkg/client"

	// velerov1api "github.com/vmware-tanzu/velero/pkg/apis/velero/v1" // This is the duplicate import, keeping it commented. The primary import is at line 27.
	"github.com/vmware-tanzu/velero/pkg/builder"
	veleroclientpkg "github.com/vmware-tanzu/velero/pkg/client"
	"github.com/vmware-tanzu/velero/pkg/cmd/util/output"
	"github.com/vmware-tanzu/velero/pkg/generated/clientset/versioned"
	"github.com/vmware-tanzu/velero/pkg/generated/clientset/versioned/fake"
	fakeveleroclient "github.com/vmware-tanzu/velero/pkg/generated/clientset/versioned/fake"
)

const testNamespace = "velero"

// mockKubeClient is a mock for sigs.k8s.io/controller-runtime/pkg/client.Client
type mockKubeClient struct {
	kbclient.Client 
	listError       error
	namespaces      []v1core.Namespace
	listCalled      bool
	backupStorageLocations map[types.NamespacedName]*velerov1api.BackupStorageLocation
}

func newMockKubeClient(namespaces []v1core.Namespace) *mockKubeClient {
	return &mockKubeClient{
		namespaces:             namespaces,
		backupStorageLocations: make(map[types.NamespacedName]*velerov1api.BackupStorageLocation),
	}
}

func (m *mockKubeClient) List(ctx context.Context, list kbclient.ObjectList, opts ...kbclient.ListOption) error {
	m.listCalled = true
	if m.listError != nil {
		return m.listError
	}
	nsList, ok := list.(*v1core.NamespaceList)
	if !ok {
		return fmt.Errorf("mockKubeClient: expected *v1core.NamespaceList, got %T", list)
	}
	nsList.Items = m.namespaces
	return nil
}

func (m *mockKubeClient) Get(ctx context.Context, key types.NamespacedName, obj kbclient.Object) error { 
	if bsl, ok := obj.(*velerov1api.BackupStorageLocation); ok {
		if val, found := m.backupStorageLocations[key]; found {
			*bsl = *val
			return nil
		}
		return fmt.Errorf("mockKubeClient: BackupStorageLocation %s/%s not found", key.Namespace, key.Name)
	}
	return fmt.Errorf("mockKubeClient: Get called for unhandled type %T", obj)
}
func (m *mockKubeClient) Create(ctx context.Context, obj kbclient.Object, opts ...kbclient.CreateOption) error { return nil }
func (m *mockKubeClient) Delete(ctx context.Context, obj kbclient.Object, opts ...kbclient.DeleteOption) error { return nil }
func (m *mockKubeClient) Update(ctx context.Context, obj kbclient.Object, opts ...kbclient.UpdateOption) error { return nil }
func (m *mockKubeClient) Patch(ctx context.Context, obj kbclient.Object, patch kbclient.Patch, opts ...kbclient.PatchOption) error { return nil }
func (m *mockKubeClient) DeleteAllOf(ctx context.Context, obj kbclient.Object, opts ...kbclient.DeleteAllOfOption) error { return nil }
func (m *mockKubeClient) Scheme() *runtime.Scheme { return runtime.NewScheme() } 
func (m *mockKubeClient) RESTMapper() meta.RESTMapper { return nil }            
func (m *mockKubeClient) Status() kbclient.StatusWriter { return &mockStatusWriter{} }

type mockStatusWriter struct{}
func (msw *mockStatusWriter) Update(ctx context.Context, obj kbclient.Object, opts ...kbclient.UpdateOption) error { return nil }
func (msw *mockStatusWriter) Patch(ctx context.Context, obj kbclient.Object, patch kbclient.Patch, opts ...kbclient.PatchOption) error { return nil }

// mockClientFactory is a mock for client.Factory
type mockClientFactory struct {
	veleroclientpkg.Factory 
	kbClient             kbclient.Client
	veleroClientSet      versioned.Interface 
	errKubeClient        error
	namespace            string
}

func newMockClientFactory(kbClient kbclient.Client, veleroCS versioned.Interface, ns string) *mockClientFactory { 
	return &mockClientFactory{
		kbClient:        kbClient,
		veleroClientSet: veleroCS,
		namespace:       ns,
	}
}

func (f *mockClientFactory) KubebuilderClient() (kbclient.Client, error) {
	if f.errKubeClient != nil {
		return nil, f.errKubeClient
	}
	return f.kbClient, nil
}

func (f *mockClientFactory) KubeClient() (kubernetes.Interface, error) {
	return fakekube.NewSimpleClientset(), nil 
}

func (f *mockClientFactory) Client() (versioned.Interface, error) { 
	if f.veleroClientSet == nil {
		return fakeveleroclient.NewSimpleClientset(), nil
	}
	return f.veleroClientSet, nil
}

func (f *mockClientFactory) Namespace() string {
	if f.namespace == "" {
		return testNamespace 
	}
	return f.namespace
}

func TestCreateOptions_BuildBackup(t *testing.T) {
	o := NewCreateOptions()
	o.Labels.Set("velero.io/test=true")
	o.OrderedResources = "pods=p1,p2;persistentvolumeclaims=pvc1,pvc2"
	orders, err := ParseOrderedResources(o.OrderedResources)
	o.CSISnapshotTimeout = 20 * time.Minute
	assert.NoError(t, err)

	backup, err := o.BuildBackup(testNamespace)
	assert.NoError(t, err)

	assert.Equal(t, velerov1api.BackupSpec{
		TTL:                     metav1.Duration{Duration: o.TTL},
		IncludedNamespaces:      []string(o.IncludeNamespaces),
		SnapshotVolumes:         o.SnapshotVolumes.Value,
		IncludeClusterResources: o.IncludeClusterResources.Value,
		OrderedResources:        orders,
		CSISnapshotTimeout:      metav1.Duration{Duration: o.CSISnapshotTimeout},
	}, backup.Spec)

	assert.Equal(t, map[string]string{
		"velero.io/test": "true",
	}, backup.GetLabels())
	assert.Equal(t, map[string]string{
		"pods":                   "p1,p2",
		"persistentvolumeclaims": "pvc1,pvc2",
	}, backup.Spec.OrderedResources)
}

func TestCreateOptions_BuildBackupFromSchedule(t *testing.T) {
	o := NewCreateOptions()
	o.FromSchedule = "test"
	o.client = fake.NewSimpleClientset()

	t.Run("inexistent schedule", func(t *testing.T) {
		_, err := o.BuildBackup(testNamespace)
		assert.Error(t, err)
	})

	expectedBackupSpec := builder.ForBackup("test", testNamespace).IncludedNamespaces("test").Result().Spec
	schedule := builder.ForSchedule(testNamespace, "test").Template(expectedBackupSpec).ObjectMeta(builder.WithLabels("velero.io/test", "true"), builder.WithAnnotations("velero.io/test", "true")).Result()
	o.client.VeleroV1().Schedules(testNamespace).Create(context.TODO(), schedule, metav1.CreateOptions{})

	t.Run("existing schedule", func(t *testing.T) {
		backup, err := o.BuildBackup(testNamespace)
		assert.NoError(t, err)

		assert.Equal(t, expectedBackupSpec, backup.Spec)
		assert.Equal(t, map[string]string{
			"velero.io/test":              "true",
			velerov1api.ScheduleNameLabel: "test",
		}, backup.GetLabels())
		assert.Equal(t, map[string]string{
			"velero.io/test": "true",
		}, backup.GetAnnotations())
	})

	t.Run("command line labels take precedence over schedule labels", func(t *testing.T) {
		o.Labels.Set("velero.io/test=yes,custom-label=true")
		backup, err := o.BuildBackup(testNamespace)
		assert.NoError(t, err)

		assert.Equal(t, expectedBackupSpec, backup.Spec)
		assert.Equal(t, map[string]string{
			"velero.io/test":              "yes",
			velerov1api.ScheduleNameLabel: "test",
			"custom-label":                "true",
		}, backup.GetLabels())
	})
}

func TestCreateOptions_OrderedResources(t *testing.T) {
	orderedResources, err := ParseOrderedResources("pods= ns1/p1; ns1/p2; persistentvolumeclaims=ns2/pvc1, ns2/pvc2")
	assert.NotNil(t, err)

	orderedResources, err = ParseOrderedResources("pods= ns1/p1,ns1/p2 ; persistentvolumeclaims=ns2/pvc1,ns2/pvc2")
	assert.NoError(t, err)

	expectedResources := map[string]string{
		"pods":                   "ns1/p1,ns1/p2",
		"persistentvolumeclaims": "ns2/pvc1,ns2/pvc2",
	}
	assert.Equal(t, orderedResources, expectedResources)

	orderedResources, err = ParseOrderedResources("pods= ns1/p1,ns1/p2 ; persistentvolumes=pv1,pv2")
	assert.NoError(t, err)

	expectedMixedResources := map[string]string{
		"pods":              "ns1/p1,ns1/p2",
		"persistentvolumes": "pv1,pv2",
	}
	assert.Equal(t, orderedResources, expectedMixedResources)

}

func TestCreateOptions_Run_NamespaceWildcardResolution(t *testing.T) {
	defaultClusterNamespaces := []v1core.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kube-public"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "velero"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "app-prod"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "app-dev"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "monitoring"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "user-test-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "user-test-2"}},
	}

	tests := []struct {
		name                       string
		includeNamespaces          []string
		fromScheduleIsSet          bool // Changed from 'fromSchedule bool' to avoid confusion with o.FromSchedule string
		fromScheduleName           string
		clusterNamespaces          []v1core.Namespace
		expectedResolvedNamespaces []string 
		expectClientListCall       bool
		listShouldError            bool
		expectedRunErrorSubstring  string
		setupVeleroClient          func(veleroCS *fakeveleroclient.Clientset, namespace string) 
		createOptsCustomizer       func(*CreateOptions)
	}{
		{
			name:                       "match all with *",
			includeNamespaces:          []string{"*"},
			clusterNamespaces:          defaultClusterNamespaces,
			expectedResolvedNamespaces: []string{"app-dev", "app-prod", "default", "kube-public", "kube-system", "monitoring", "user-test-1", "user-test-2", "velero"},
			expectClientListCall:       true,
		},
		{
			name:                       "match specific with kube-*",
			includeNamespaces:          []string{"kube-*"},
			clusterNamespaces:          defaultClusterNamespaces,
			expectedResolvedNamespaces: []string{"kube-public", "kube-system"},
			expectClientListCall:       true,
		},
		{
			name:                       "no matching namespaces for pattern",
			includeNamespaces:          []string{"nonexistent-*"},
			clusterNamespaces:          defaultClusterNamespaces,
			expectedResolvedNamespaces: []string{},
			expectClientListCall:       true,
		},
		{
			name:                       "literal pattern 'app[' which is an invalid K8s name",
			includeNamespaces:          []string{"app["},
			clusterNamespaces:          defaultClusterNamespaces,
			expectedResolvedNamespaces: []string{"placeholder"}, // Should not be changed from initial if Validate fails
			expectClientListCall:       false, // Run() is not reached if Validate() fails
			expectedRunErrorSubstring:  "invalid namespace \"app[\"", // Error from collections.ValidateNamespaceIncludesExcludes
		},
		{
			name:                       "empty include-namespaces (ResolvedNamespaces is empty, server interprets as all)",
			includeNamespaces:          []string{},
			clusterNamespaces:          defaultClusterNamespaces,
			expectedResolvedNamespaces: []string{},
			expectClientListCall:       true, 
		},
		{
			name:                       "mixed exact and wildcard, with duplicates",
			includeNamespaces:          []string{"default", "kube-*", "app-prod", "user-test-*", "default"},
			clusterNamespaces:          defaultClusterNamespaces,
			expectedResolvedNamespaces: []string{"app-prod", "default", "kube-public", "kube-system", "user-test-1", "user-test-2"},
			expectClientListCall:       true,
		},
		{
			name:                       "exact match for non-existent namespace (added to resolved, server validates)",
			includeNamespaces:          []string{"non-existent-ns"},
			clusterNamespaces:          defaultClusterNamespaces,
			expectedResolvedNamespaces: []string{"non-existent-ns"},
			expectClientListCall:       true,
		},
		{
			name:                 "FromSchedule is true, wildcard logic skipped",
			includeNamespaces:    []string{"*"}, 
			fromScheduleIsSet:    true,
			fromScheduleName:     "my-schedule",
			clusterNamespaces:    defaultClusterNamespaces,
			expectClientListCall: false,
			expectedResolvedNamespaces: nil, 
			setupVeleroClient: func(veleroCS *fakeveleroclient.Clientset, ns string) {
				schedule := builder.ForSchedule(ns, "my-schedule").Result()
				veleroCS.VeleroV1().Schedules(ns).Create(context.Background(), schedule, metav1.CreateOptions{})
			},
		},
		{
			name:                      "Error listing namespaces",
			includeNamespaces:         []string{"*"},
			listShouldError:           true,
			expectedRunErrorSubstring: "error listing namespaces", // This error comes from Run()
			expectClientListCall:      true,
			// When Run errors early, ResolvedNamespaces keeps its initial value.
			expectedResolvedNamespaces: []string{"placeholder"}, // Matches initial dummy value
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := NewCreateOptions()
			o.IncludeNamespaces = tc.includeNamespaces
			if tc.fromScheduleIsSet {
				o.FromSchedule = tc.fromScheduleName
				o.ResolvedNamespaces = nil // Ensure it's nil initially if FromSchedule is used
			} else {
				o.FromSchedule = "" // Ensure it's empty if not FromSchedule test
				o.ResolvedNamespaces = []string{"placeholder"} // Set dummy to check if overwritten
			}

			if o.FromSchedule == "" { // Set default name if not from schedule
				o.Name = "test-backup"
			}
			if tc.createOptsCustomizer != nil {
				tc.createOptsCustomizer(o)
			}

			mockKubeCli := newMockKubeClient(tc.clusterNamespaces)
			if tc.listShouldError {
				mockKubeCli.listError = fmt.Errorf("simulated list error")
			}

			veleroFakeCS := fakeveleroclient.NewSimpleClientset()
			if tc.setupVeleroClient != nil {
				tc.setupVeleroClient(veleroFakeCS, testNamespace)
			}
			
			// Ensure o.client is set for Validate and other parts of Run
			o.client = veleroFakeCS 

			mockFactory := newMockClientFactory(mockKubeCli, veleroFakeCS, testNamespace)
			
			o.StorageLocation = ""      
			o.SnapshotLocations = nil 

			cmd := &cobra.Command{}
			output.BindFlags(cmd.Flags()) 

			var completeArgs []string
			if o.FromSchedule == "" && o.Name != "" { 
				completeArgs = append(completeArgs, o.Name)
			} else if o.FromSchedule != "" && o.Name == "" {
				// For auto-generated name from schedule
			}
			
			err := o.Complete(completeArgs, mockFactory)
			assert.NoError(t, err, "o.Complete failed")
			
			// Validate can use kbclient for BSL checks, ensure it's available on factory
			// o.client is velero client, used for VSL checks.
			valErr := o.Validate(cmd, completeArgs, mockFactory)
			if tc.expectedRunErrorSubstring != "" {
				// If an error is expected (either from Validate or Run)
				if valErr != nil { // Error from Validate
					assert.Error(t, valErr, "Expected an error from Validate for test: %s", tc.name)
					assert.Contains(t, valErr.Error(), tc.expectedRunErrorSubstring, "Validate error substring mismatch for test: %s", tc.name)
					// If Validate errors as expected, and it's not a list error (which implies Run) then return
					if !tc.listShouldError {
						// Also ensure ResolvedNamespaces is what it was initially
						sort.Strings(o.ResolvedNamespaces) // it was placeholder
						sort.Strings(tc.expectedResolvedNamespaces) // this should be placeholder for this case
						assert.Equal(t, tc.expectedResolvedNamespaces, o.ResolvedNamespaces, "ResolvedNamespaces should be unchanged if Validate fails for test: %s", tc.name)
						return
					}
				} else { // No error from Validate, so error must be from Run
					runErr := o.Run(cmd, mockFactory)
					assert.Error(t, runErr, "Expected an error from Run for test: %s", tc.name)
					if runErr != nil {
						assert.Contains(t, runErr.Error(), tc.expectedRunErrorSubstring, "Run error substring mismatch for test: %s", tc.name)
					}
				}
			} else {
				// No error expected from Validate or Run
				assert.NoError(t, valErr, "o.Validate failed unexpectedly for test: %s", tc.name)
				runErr := o.Run(cmd, mockFactory)
				assert.NoError(t, runErr, "o.Run returned an unexpected error for test: %s", tc.name)
			}
			
			assert.Equal(t, tc.expectClientListCall, mockKubeCli.listCalled, "kubernetes client List() call expectation mismatch for test: %s", tc.name)

			sort.Strings(o.ResolvedNamespaces)
			sort.Strings(tc.expectedResolvedNamespaces)
			assert.Equal(t, tc.expectedResolvedNamespaces, o.ResolvedNamespaces, "ResolvedNamespaces mismatch for test: %s", tc.name)
		})
	}
}
