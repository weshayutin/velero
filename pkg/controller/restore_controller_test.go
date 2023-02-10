/*
Copyright the Velero contributors.

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

package controller

import (
	"bytes"
	"context"
	"io/ioutil"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	velerov1api "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"github.com/vmware-tanzu/velero/pkg/builder"
	"github.com/vmware-tanzu/velero/pkg/generated/clientset/versioned/fake"
	informers "github.com/vmware-tanzu/velero/pkg/generated/informers/externalversions"
	"github.com/vmware-tanzu/velero/pkg/metrics"
	persistencemocks "github.com/vmware-tanzu/velero/pkg/persistence/mocks"
	"github.com/vmware-tanzu/velero/pkg/plugin/clientmgmt"
	"github.com/vmware-tanzu/velero/pkg/plugin/framework"
	pluginmocks "github.com/vmware-tanzu/velero/pkg/plugin/mocks"
	isv1 "github.com/vmware-tanzu/velero/pkg/plugin/velero/item_snapshotter/v1"
	riav2 "github.com/vmware-tanzu/velero/pkg/plugin/velero/restoreitemaction/v2"
	pkgrestore "github.com/vmware-tanzu/velero/pkg/restore"
	velerotest "github.com/vmware-tanzu/velero/pkg/test"
	"github.com/vmware-tanzu/velero/pkg/util/logging"
	"github.com/vmware-tanzu/velero/pkg/volume"
)

func TestFetchBackupInfo(t *testing.T) {

	tests := []struct {
		name              string
		backupName        string
		informerLocations []*velerov1api.BackupStorageLocation
		informerBackups   []*velerov1api.Backup
		backupStoreBackup *velerov1api.Backup
		backupStoreError  error
		expectedRes       *velerov1api.Backup
		expectedErr       bool
	}{
		{
			name:              "lister has backup",
			backupName:        "backup-1",
			informerLocations: []*velerov1api.BackupStorageLocation{builder.ForBackupStorageLocation("velero", "default").Provider("myCloud").Bucket("bucket").Result()},
			informerBackups:   []*velerov1api.Backup{defaultBackup().StorageLocation("default").Result()},
			expectedRes:       defaultBackup().StorageLocation("default").Result(),
		},
		{
			name:              "lister does not have a backup, but backupSvc does",
			backupName:        "backup-1",
			backupStoreBackup: defaultBackup().StorageLocation("default").Result(),
			informerLocations: []*velerov1api.BackupStorageLocation{builder.ForBackupStorageLocation("velero", "default").Provider("myCloud").Bucket("bucket").Result()},
			informerBackups:   []*velerov1api.Backup{defaultBackup().StorageLocation("default").Result()},
			expectedRes:       defaultBackup().StorageLocation("default").Result(),
		},
		{
			name:             "no backup",
			backupName:       "backup-1",
			backupStoreError: errors.New("no backup here"),
			expectedErr:      true,
		},
	}

	formatFlag := logging.FormatText

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				client          = fake.NewSimpleClientset()
				fakeClient      = velerotest.NewFakeControllerRuntimeClient(t)
				restorer        = &fakeRestorer{kbClient: fakeClient}
				sharedInformers = informers.NewSharedInformerFactory(client, 0)
				logger          = velerotest.NewLogger()
				pluginManager   = &pluginmocks.Manager{}
				backupStore     = &persistencemocks.BackupStore{}
			)

			defer restorer.AssertExpectations(t)
			defer backupStore.AssertExpectations(t)

			c := NewRestoreController(
				velerov1api.DefaultNamespace,
				sharedInformers.Velero().V1().Restores(),
				restorer,
				fakeClient,
				logger,
				logrus.InfoLevel,
				func(logrus.FieldLogger) clientmgmt.Manager { return pluginManager },
				NewFakeSingleObjectBackupStoreGetter(backupStore),
				metrics.NewServerMetrics(),
				formatFlag,
			).(*restoreController)

			if test.backupStoreError == nil {
				for _, itm := range test.informerLocations {
					require.NoError(t, c.kbClient.Create(context.Background(), itm))
				}

				for _, itm := range test.informerBackups {
					assert.NoError(t, c.kbClient.Create(context.Background(), itm))
				}
			}

			if test.backupStoreBackup != nil && test.backupStoreError != nil {
				panic("developer error - only one of backupStoreBackup, backupStoreError can be non-nil")
			}

			if test.backupStoreError != nil {
				// TODO why do I need .Maybe() here?
				backupStore.On("GetBackupMetadata", test.backupName).Return(nil, test.backupStoreError).Maybe()
			}
			if test.backupStoreBackup != nil {
				// TODO why do I need .Maybe() here?
				backupStore.On("GetBackupMetadata", test.backupName).Return(test.backupStoreBackup, nil).Maybe()
			}

			info, err := c.fetchBackupInfo(test.backupName, pluginManager)

			require.Equal(t, test.expectedErr, err != nil)
			if test.expectedRes != nil {
				assert.Equal(t, test.expectedRes.Spec, info.backup.Spec)
			}
		})
	}
}

func TestProcessQueueItemSkips(t *testing.T) {
	tests := []struct {
		name        string
		restoreKey  string
		restore     *velerov1api.Restore
		expectError bool
	}{
		{
			name:       "invalid key returns error",
			restoreKey: "invalid/key/value",
		},
		{
			name:        "missing restore returns error",
			restoreKey:  "foo/bar",
			expectError: true,
		},
		{
			name:       "restore with phase InProgress does not get processed",
			restoreKey: "foo/bar",
			restore:    builder.ForRestore("foo", "bar").Phase(velerov1api.RestorePhaseInProgress).Result(),
		},
		{
			name:       "restore with phase Completed does not get processed",
			restoreKey: "foo/bar",
			restore:    builder.ForRestore("foo", "bar").Phase(velerov1api.RestorePhaseCompleted).Result(),
		},
		{
			name:       "restore with phase FailedValidation does not get processed",
			restoreKey: "foo/bar",
			restore:    builder.ForRestore("foo", "bar").Phase(velerov1api.RestorePhaseFailedValidation).Result(),
		},
	}

	formatFlag := logging.FormatText

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				client          = fake.NewSimpleClientset()
				fakeClient      = velerotest.NewFakeControllerRuntimeClient(t)
				restorer        = &fakeRestorer{kbClient: fakeClient}
				sharedInformers = informers.NewSharedInformerFactory(client, 0)
				logger          = velerotest.NewLogger()
			)

			c := NewRestoreController(
				velerov1api.DefaultNamespace,
				sharedInformers.Velero().V1().Restores(),
				restorer,
				fakeClient,
				logger,
				logrus.InfoLevel,
				nil,
				nil, // backupStoreGetter
				metrics.NewServerMetrics(),
				formatFlag,
			).(*restoreController)

			if test.restore != nil {
				c.kbClient.Create(context.Background(), test.restore)
			}

			err := c.processQueueItem(test.restoreKey)

			assert.Equal(t, test.expectError, err != nil)
		})
	}
}

func TestProcessQueueItem(t *testing.T) {

	defaultStorageLocation := builder.ForBackupStorageLocation("velero", "default").Provider("myCloud").Bucket("bucket").Result()

	now, err := time.Parse(time.RFC1123Z, time.RFC1123Z)
	require.NoError(t, err)
	now = now.Local()
	timestamp := metav1.NewTime(now)
	assert.NotNil(t, timestamp)

	tests := []struct {
		name                            string
		restoreKey                      string
		location                        *velerov1api.BackupStorageLocation
		restore                         *velerov1api.Restore
		backup                          *velerov1api.Backup
		restorerError                   error
		expectedErr                     bool
		expectedPhase                   string
		expectedStartTime               *metav1.Time
		expectedCompletedTime           *metav1.Time
		expectedValidationErrors        []string
		expectedRestoreErrors           int
		expectedRestorerCall            *velerov1api.Restore
		backupStoreGetBackupMetadataErr error
		backupStoreGetBackupContentsErr error
		putRestoreLogErr                error
		expectedFinalPhase              string
	}{
		{
			name:                     "restore with both namespace in both includedNamespaces and excludedNamespaces fails validation",
			location:                 defaultStorageLocation,
			restore:                  NewRestore("foo", "bar", "backup-1", "another-1", "*", velerov1api.RestorePhaseNew).ExcludedNamespaces("another-1").Result(),
			backup:                   defaultBackup().StorageLocation("default").Result(),
			expectedErr:              false,
			expectedPhase:            string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{"Invalid included/excluded namespace lists: excludes list cannot contain an item in the includes list: another-1"},
		},
		{
			name:                     "restore with resource in both includedResources and excludedResources fails validation",
			location:                 defaultStorageLocation,
			restore:                  NewRestore("foo", "bar", "backup-1", "*", "a-resource", velerov1api.RestorePhaseNew).ExcludedResources("a-resource").Result(),
			backup:                   defaultBackup().StorageLocation("default").Result(),
			expectedErr:              false,
			expectedPhase:            string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{"Invalid included/excluded resource lists: excludes list cannot contain an item in the includes list: a-resource"},
		},
		{
			name:                     "new restore with empty backup and schedule names fails validation",
			restore:                  NewRestore("foo", "bar", "", "ns-1", "", velerov1api.RestorePhaseNew).Result(),
			expectedErr:              false,
			expectedPhase:            string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{"Either a backup or schedule must be specified as a source for the restore, but not both"},
		},
		{
			name:                     "new restore with backup and schedule names provided fails validation",
			restore:                  NewRestore("foo", "bar", "backup-1", "ns-1", "", velerov1api.RestorePhaseNew).Schedule("sched-1").Result(),
			expectedErr:              false,
			expectedPhase:            string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{"Either a backup or schedule must be specified as a source for the restore, but not both"},
		},
		{
			name:                     "new restore with labelSelector as well as orLabelSelector fails validation",
			location:                 defaultStorageLocation,
			restore:                  NewRestore("foo", "bar", "backup-1", "ns-1", "", velerov1api.RestorePhaseNew).LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}).OrLabelSelector([]*metav1.LabelSelector{{MatchLabels: map[string]string{"a1": "b1"}}, {MatchLabels: map[string]string{"a2": "b2"}}, {MatchLabels: map[string]string{"a3": "b3"}}, {MatchLabels: map[string]string{"a4": "b4"}}}).Result(),
			backup:                   defaultBackup().StorageLocation("default").Result(),
			expectedErr:              false,
			expectedValidationErrors: []string{"encountered labelSelector as well as orLabelSelectors in restore spec, only one can be specified"},
			expectedPhase:            string(velerov1api.RestorePhaseFailedValidation),
		},
		{
			name:                  "valid restore with schedule name gets executed",
			location:              defaultStorageLocation,
			restore:               NewRestore("foo", "bar", "", "ns-1", "", velerov1api.RestorePhaseNew).Schedule("sched-1").Result(),
			backup:                defaultBackup().StorageLocation("default").ObjectMeta(builder.WithLabels(velerov1api.ScheduleNameLabel, "sched-1")).Phase(velerov1api.BackupPhaseCompleted).Result(),
			expectedErr:           false,
			expectedPhase:         string(velerov1api.RestorePhaseInProgress),
			expectedStartTime:     &timestamp,
			expectedCompletedTime: &timestamp,
			expectedRestorerCall:  NewRestore("foo", "bar", "backup-1", "ns-1", "", velerov1api.RestorePhaseInProgress).Schedule("sched-1").Result(),
		},
		{
			name:                            "restore with non-existent backup name fails",
			restore:                         NewRestore("foo", "bar", "backup-1", "ns-1", "*", velerov1api.RestorePhaseNew).Result(),
			expectedErr:                     false,
			expectedPhase:                   string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors:        []string{"Error retrieving backup: backup.velero.io \"backup-1\" not found"},
			backupStoreGetBackupMetadataErr: errors.New("no backup here"),
		},
		{
			name:                  "restorer throwing an error causes the restore to fail",
			location:              defaultStorageLocation,
			restore:               NewRestore("foo", "bar", "backup-1", "ns-1", "", velerov1api.RestorePhaseNew).Result(),
			backup:                defaultBackup().StorageLocation("default").Result(),
			restorerError:         errors.New("blarg"),
			expectedErr:           false,
			expectedPhase:         string(velerov1api.RestorePhaseInProgress),
			expectedFinalPhase:    string(velerov1api.RestorePhasePartiallyFailed),
			expectedStartTime:     &timestamp,
			expectedCompletedTime: &timestamp,
			expectedRestoreErrors: 1,
			expectedRestorerCall:  NewRestore("foo", "bar", "backup-1", "ns-1", "", velerov1api.RestorePhaseInProgress).Result(),
		},
		{
			name:                  "valid restore gets executed",
			location:              defaultStorageLocation,
			restore:               NewRestore("foo", "bar", "backup-1", "ns-1", "", velerov1api.RestorePhaseNew).Result(),
			backup:                defaultBackup().StorageLocation("default").Result(),
			expectedErr:           false,
			expectedPhase:         string(velerov1api.RestorePhaseInProgress),
			expectedStartTime:     &timestamp,
			expectedCompletedTime: &timestamp,
			expectedRestorerCall:  NewRestore("foo", "bar", "backup-1", "ns-1", "", velerov1api.RestorePhaseInProgress).Result(),
		},
		{
			name:          "restoration of nodes is not supported",
			location:      defaultStorageLocation,
			restore:       NewRestore("foo", "bar", "backup-1", "ns-1", "nodes", velerov1api.RestorePhaseNew).Result(),
			backup:        defaultBackup().StorageLocation("default").Result(),
			expectedErr:   false,
			expectedPhase: string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{
				"nodes are non-restorable resources",
				"Invalid included/excluded resource lists: excludes list cannot contain an item in the includes list: nodes",
			},
		},
		{
			name:          "restoration of events is not supported",
			location:      defaultStorageLocation,
			restore:       NewRestore("foo", "bar", "backup-1", "ns-1", "events", velerov1api.RestorePhaseNew).Result(),
			backup:        defaultBackup().StorageLocation("default").Result(),
			expectedErr:   false,
			expectedPhase: string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{
				"events are non-restorable resources",
				"Invalid included/excluded resource lists: excludes list cannot contain an item in the includes list: events",
			},
		},
		{
			name:          "restoration of events.events.k8s.io is not supported",
			location:      defaultStorageLocation,
			restore:       NewRestore("foo", "bar", "backup-1", "ns-1", "events.events.k8s.io", velerov1api.RestorePhaseNew).Result(),
			backup:        defaultBackup().StorageLocation("default").Result(),
			expectedErr:   false,
			expectedPhase: string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{
				"events.events.k8s.io are non-restorable resources",
				"Invalid included/excluded resource lists: excludes list cannot contain an item in the includes list: events.events.k8s.io",
			},
		},
		{
			name:          "restoration of backups.velero.io is not supported",
			location:      defaultStorageLocation,
			restore:       NewRestore("foo", "bar", "backup-1", "ns-1", "backups.velero.io", velerov1api.RestorePhaseNew).Result(),
			backup:        defaultBackup().StorageLocation("default").Result(),
			expectedErr:   false,
			expectedPhase: string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{
				"backups.velero.io are non-restorable resources",
				"Invalid included/excluded resource lists: excludes list cannot contain an item in the includes list: backups.velero.io",
			},
		},
		{
			name:          "restoration of restores.velero.io is not supported",
			location:      defaultStorageLocation,
			restore:       NewRestore("foo", "bar", "backup-1", "ns-1", "restores.velero.io", velerov1api.RestorePhaseNew).Result(),
			backup:        defaultBackup().StorageLocation("default").Result(),
			expectedErr:   false,
			expectedPhase: string(velerov1api.RestorePhaseFailedValidation),
			expectedValidationErrors: []string{
				"restores.velero.io are non-restorable resources",
				"Invalid included/excluded resource lists: excludes list cannot contain an item in the includes list: restores.velero.io",
			},
		},
		{
			name:                            "backup download error results in failed restore",
			location:                        defaultStorageLocation,
			restore:                         NewRestore(velerov1api.DefaultNamespace, "bar", "backup-1", "ns-1", "", velerov1api.RestorePhaseNew).Result(),
			expectedPhase:                   string(velerov1api.RestorePhaseInProgress),
			expectedFinalPhase:              string(velerov1api.RestorePhaseFailed),
			expectedStartTime:               &timestamp,
			expectedCompletedTime:           &timestamp,
			backupStoreGetBackupContentsErr: errors.New("Couldn't download backup"),
			backup:                          defaultBackup().StorageLocation("default").Result(),
		},
	}

	formatFlag := logging.FormatText

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				client          = fake.NewSimpleClientset()
				fakeClient      = velerotest.NewFakeControllerRuntimeClientBuilder(t).Build()
				restorer        = &fakeRestorer{kbClient: fakeClient}
				sharedInformers = informers.NewSharedInformerFactory(client, 0)
				logger          = velerotest.NewLogger()
				pluginManager   = &pluginmocks.Manager{}
				backupStore     = &persistencemocks.BackupStore{}
			)

			defer restorer.AssertExpectations(t)
			defer backupStore.AssertExpectations(t)
			defer func() {
				// reset defaultStorageLocation resourceVersion
				defaultStorageLocation.ObjectMeta.ResourceVersion = ""
			}()

			c := NewRestoreController(
				velerov1api.DefaultNamespace,
				sharedInformers.Velero().V1().Restores(),
				restorer,
				fakeClient,
				logger,
				logrus.InfoLevel,
				func(logrus.FieldLogger) clientmgmt.Manager { return pluginManager },
				NewFakeSingleObjectBackupStoreGetter(backupStore),
				metrics.NewServerMetrics(),
				formatFlag,
			).(*restoreController)

			c.clock = clocktesting.NewFakeClock(now)
			if test.location != nil {
				require.NoError(t, fakeClient.Create(context.Background(), test.location))
			}
			if test.backup != nil {
				assert.NoError(t, c.kbClient.Create(context.Background(), test.backup))
			}

			if test.restore != nil {
				require.NoError(t, c.kbClient.Create(context.Background(), test.restore))
			}

			var warnings, errors pkgrestore.Result
			if test.restorerError != nil {
				errors.Namespaces = map[string][]string{"ns-1": {test.restorerError.Error()}}
			}
			if test.putRestoreLogErr != nil {
				errors.Velero = append(errors.Velero, "error uploading log file to object storage: "+test.putRestoreLogErr.Error())
			}
			if test.expectedRestorerCall != nil {
				backupStore.On("GetBackupContents", test.backup.Name).Return(ioutil.NopCloser(bytes.NewReader([]byte("hello world"))), nil)

				restorer.On("RestoreWithResolvers", mock.Anything, mock.Anything, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(warnings, errors)

				backupStore.On("PutRestoreLog", test.backup.Name, test.restore.Name, mock.Anything).Return(test.putRestoreLogErr)

				backupStore.On("PutRestoreResults", test.backup.Name, test.restore.Name, mock.Anything).Return(nil)

				volumeSnapshots := []*volume.Snapshot{
					{
						Spec: volume.SnapshotSpec{
							PersistentVolumeName: "test-pv",
							BackupName:           test.backup.Name,
						},
					},
				}
				backupStore.On("GetBackupVolumeSnapshots", test.backup.Name).Return(volumeSnapshots, nil)
			}

			var (
				key = test.restoreKey
				err error
			)
			if key == "" && test.restore != nil {
				key, err = cache.MetaNamespaceKeyFunc(test.restore)
				if err != nil {
					panic(err)
				}
			}

			if test.backupStoreGetBackupMetadataErr != nil {
				// TODO why do I need .Maybe() here?
				backupStore.On("GetBackupMetadata", test.restore.Spec.BackupName).Return(nil, test.backupStoreGetBackupMetadataErr).Maybe()
			}

			if test.backupStoreGetBackupContentsErr != nil {
				// TODO why do I need .Maybe() here?
				backupStore.On("GetBackupContents", test.restore.Spec.BackupName).Return(nil, test.backupStoreGetBackupContentsErr).Maybe()
			}

			if test.restore != nil {
				pluginManager.On("GetRestoreItemActionsV2").Return(nil, nil)
				pluginManager.On("GetItemSnapshotters").Return([]isv1.ItemSnapshotter{}, nil)
				pluginManager.On("CleanupClients")
			}

			err = c.processQueueItem(key)

			assert.Equal(t, test.expectedErr, err != nil, "got error %v", err)

			if test.expectedPhase == "" {
				return
			}

			// struct and func for decoding patch content
			type SpecPatch struct {
				BackupName string `json:"backupName"`
			}

			type StatusPatch struct {
				Phase               velerov1api.RestorePhase `json:"phase"`
				ValidationErrors    []string                 `json:"validationErrors"`
				Errors              int                      `json:"errors"`
				StartTimestamp      *metav1.Time             `json:"startTimestamp"`
				CompletionTimestamp *metav1.Time             `json:"completionTimestamp"`
			}

			type Patch struct {
				Spec   SpecPatch   `json:"spec,omitempty"`
				Status StatusPatch `json:"status"`
			}

			expected := Patch{
				Status: StatusPatch{
					Phase:            velerov1api.RestorePhase(test.expectedPhase),
					ValidationErrors: test.expectedValidationErrors,
				},
			}

			if test.restore.Spec.ScheduleName != "" && test.backup != nil {
				expected.Spec = SpecPatch{
					BackupName: test.backup.Name,
				}
			}

			if test.expectedStartTime != nil {
				expected.Status.StartTimestamp = test.expectedStartTime
			}

			// if we don't expect a restore, validate it wasn't called and exit the test
			if test.expectedRestorerCall == nil {
				assert.Empty(t, restorer.Calls)
				assert.Zero(t, restorer.calledWithArg)
				return
			}
			assert.Equal(t, 1, len(restorer.Calls))

			// validate Patch call 2 (setting phase)

			expected = Patch{
				Status: StatusPatch{
					Phase:               velerov1api.RestorePhaseCompleted,
					Errors:              test.expectedRestoreErrors,
					CompletionTimestamp: test.expectedCompletedTime,
				},
			}
			// Override our default expectations if the case requires it
			if test.expectedFinalPhase != "" {
				expected = Patch{
					Status: StatusPatch{
						Phase:               velerov1api.RestorePhase(test.expectedFinalPhase),
						Errors:              test.expectedRestoreErrors,
						CompletionTimestamp: test.expectedCompletedTime,
					},
				}
			}

			// explicitly capturing the argument passed to Restore myself because
			// I want to validate the called arg as of the time of calling, but
			// the mock stores the pointer, which gets modified after
			assert.Equal(t, test.expectedRestorerCall.Spec, restorer.calledWithArg.Spec)
			assert.Equal(t, test.expectedRestorerCall.Status.Phase, restorer.calledWithArg.Status.Phase)
		})
	}
}

func TestValidateAndCompleteWhenScheduleNameSpecified(t *testing.T) {
	formatFlag := logging.FormatText

	var (
		client          = fake.NewSimpleClientset()
		sharedInformers = informers.NewSharedInformerFactory(client, 0)
		logger          = velerotest.NewLogger()
		pluginManager   = &pluginmocks.Manager{}
		fakeClient      = velerotest.NewFakeControllerRuntimeClient(t)
		backupStore     = &persistencemocks.BackupStore{}
	)

	c := NewRestoreController(
		velerov1api.DefaultNamespace,
		sharedInformers.Velero().V1().Restores(),
		nil,
		fakeClient,
		logger,
		logrus.DebugLevel,
		func(logrus.FieldLogger) clientmgmt.Manager { return pluginManager },
		NewFakeSingleObjectBackupStoreGetter(backupStore),
		nil,
		formatFlag,
	).(*restoreController)

	restore := &velerov1api.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: velerov1api.DefaultNamespace,
			Name:      "restore-1",
		},
		Spec: velerov1api.RestoreSpec{
			ScheduleName: "schedule-1",
		},
	}

	// no backups created from the schedule: fail validation
	require.NoError(t, sharedInformers.Velero().V1().Backups().Informer().GetStore().Add(
		defaultBackup().
			ObjectMeta(builder.WithLabels(velerov1api.ScheduleNameLabel, "non-matching-schedule")).
			Phase(velerov1api.BackupPhaseCompleted).
			Result(),
	))

	c.validateAndComplete(restore, pluginManager)
	assert.Contains(t, restore.Status.ValidationErrors, "No backups found for schedule")
	assert.Empty(t, restore.Spec.BackupName)

	// no completed backups created from the schedule: fail validation
	require.NoError(t, sharedInformers.Velero().V1().Backups().Informer().GetStore().Add(
		defaultBackup().
			ObjectMeta(
				builder.WithName("backup-2"),
				builder.WithLabels(velerov1api.ScheduleNameLabel, "schedule-1"),
			).
			Phase(velerov1api.BackupPhaseInProgress).
			Result(),
	))

	c.validateAndComplete(restore, pluginManager)
	assert.Contains(t, restore.Status.ValidationErrors, "No completed backups found for schedule")
	assert.Empty(t, restore.Spec.BackupName)

	// multiple completed backups created from the schedule: use most recent
	now := time.Now()

	require.NoError(t, c.kbClient.Create(context.Background(),
		defaultBackup().
			ObjectMeta(
				builder.WithName("foo"),
				builder.WithLabels(velerov1api.ScheduleNameLabel, "schedule-1"),
			).
			StorageLocation("default").
			Phase(velerov1api.BackupPhaseCompleted).
			StartTimestamp(now).
			Result(),
	))

	location := builder.ForBackupStorageLocation("velero", "default").Provider("myCloud").Bucket("bucket").Result()
	require.NoError(t, c.kbClient.Create(context.Background(), location))

	restore = &velerov1api.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: velerov1api.DefaultNamespace,
			Name:      "restore-1",
		},
		Spec: velerov1api.RestoreSpec{
			ScheduleName: "schedule-1",
		},
	}
	c.validateAndComplete(restore, pluginManager)
	assert.Nil(t, restore.Status.ValidationErrors)
	assert.Equal(t, "foo", restore.Spec.BackupName)
}

func TestBackupXorScheduleProvided(t *testing.T) {
	r := &velerov1api.Restore{}
	assert.False(t, backupXorScheduleProvided(r))

	r.Spec.BackupName = "backup-1"
	r.Spec.ScheduleName = "schedule-1"
	assert.False(t, backupXorScheduleProvided(r))

	r.Spec.BackupName = "backup-1"
	r.Spec.ScheduleName = ""
	assert.True(t, backupXorScheduleProvided(r))

	r.Spec.BackupName = ""
	r.Spec.ScheduleName = "schedule-1"
	assert.True(t, backupXorScheduleProvided(r))
}

func TestMostRecentCompletedBackup(t *testing.T) {
	backups := []velerov1api.Backup{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "a",
			},
			Status: velerov1api.BackupStatus{
				Phase: "",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "b",
			},
			Status: velerov1api.BackupStatus{
				Phase: velerov1api.BackupPhaseNew,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "c",
			},
			Status: velerov1api.BackupStatus{
				Phase: velerov1api.BackupPhaseInProgress,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "d",
			},
			Status: velerov1api.BackupStatus{
				Phase: velerov1api.BackupPhaseFailedValidation,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "e",
			},
			Status: velerov1api.BackupStatus{
				Phase: velerov1api.BackupPhaseFailed,
			},
		},
	}

	assert.Empty(t, mostRecentCompletedBackup(backups).Name)

	now := time.Now()

	backups = append(backups, velerov1api.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
		},
		Status: velerov1api.BackupStatus{
			Phase:          velerov1api.BackupPhaseCompleted,
			StartTimestamp: &metav1.Time{Time: now},
		},
	})

	expected := velerov1api.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bar",
		},
		Status: velerov1api.BackupStatus{
			Phase:          velerov1api.BackupPhaseCompleted,
			StartTimestamp: &metav1.Time{Time: now.Add(time.Second)},
		},
	}
	backups = append(backups, expected)

	assert.Equal(t, expected, mostRecentCompletedBackup(backups))
}

func NewRestore(ns, name, backup, includeNS, includeResource string, phase velerov1api.RestorePhase) *builder.RestoreBuilder {
	restore := builder.ForRestore(ns, name).Phase(phase).Backup(backup)

	if includeNS != "" {
		restore = restore.IncludedNamespaces(includeNS)
	}

	if includeResource != "" {
		restore = restore.IncludedResources(includeResource)
	}

	restore.ExcludedResources(nonRestorableResources...)

	return restore
}

type fakeRestorer struct {
	mock.Mock
	calledWithArg velerov1api.Restore
	kbClient      client.Client
}

func (r *fakeRestorer) Restore(
	info pkgrestore.Request,
	actions []riav2.RestoreItemAction,
	volumeSnapshotterGetter pkgrestore.VolumeSnapshotterGetter,
) (pkgrestore.Result, pkgrestore.Result) {
	res := r.Called(info.Log, info.Restore, info.Backup, info.BackupReader, actions)

	r.calledWithArg = *info.Restore

	return res.Get(0).(pkgrestore.Result), res.Get(1).(pkgrestore.Result)
}

func (r *fakeRestorer) RestoreWithResolvers(req pkgrestore.Request,
	resolver framework.RestoreItemActionResolverV2,
	itemSnapshotterResolver framework.ItemSnapshotterResolver,
	volumeSnapshotterGetter pkgrestore.VolumeSnapshotterGetter,
) (pkgrestore.Result, pkgrestore.Result) {
	res := r.Called(req.Log, req.Restore, req.Backup, req.BackupReader, resolver, itemSnapshotterResolver,
		r.kbClient, volumeSnapshotterGetter)

	r.calledWithArg = *req.Restore

	return res.Get(0).(pkgrestore.Result), res.Get(1).(pkgrestore.Result)
}
