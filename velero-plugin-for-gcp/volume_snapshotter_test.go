/*
Copyright 2017 the Velero contributors.

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

package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	velerotest "github.com/vmware-tanzu/velero/pkg/test"
)

func TestGetVolumeID(t *testing.T) {
	b := &VolumeSnapshotter{}

	pv := &unstructured.Unstructured{
		Object: map[string]interface{}{},
	}

	// missing spec.gcePersistentDisk -> no error
	volumeID, err := b.GetVolumeID(pv)
	require.NoError(t, err)
	assert.Equal(t, "", volumeID)

	// missing spec.gcePersistentDisk.pdName -> error
	gce := map[string]interface{}{}
	pv.Object["spec"] = map[string]interface{}{
		"gcePersistentDisk": gce,
	}
	volumeID, err = b.GetVolumeID(pv)
	assert.Error(t, err)
	assert.Equal(t, "", volumeID)

	// valid
	gce["pdName"] = "abc123"
	volumeID, err = b.GetVolumeID(pv)
	assert.NoError(t, err)
	assert.Equal(t, "abc123", volumeID)
}

func TestGetVolumeIDForCSI(t *testing.T) {
	b := &VolumeSnapshotter{
		log: logrus.New(),
	}

	cases := []struct {
		name    string
		csiJSON string
		want    string
		wantErr bool
	}{
		{
			name: "gke csi driver",
			csiJSON: `{
				"driver": "pd.csi.storage.gke.io",
				"fsType": "ext4",
				"volumeAttributes": {
					"storage.kubernetes.io/csiProvisionerIdentity": "1637243273131-8081-pd.csi.storage.gke.io"
				},
				"volumeHandle": "projects/velero-gcp/zones/us-central1-f/disks/pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			want:    "pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d",
			wantErr: false,
		},
		{
			name: "gke csi driver with invalid handle name",
			csiJSON: `{
				"driver": "pd.csi.storage.gke.io",
				"fsType": "ext4",
				"volumeHandle": "pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			want:    "",
			wantErr: true,
		},
		{
			name: "Constellation csi driver",
			csiJSON: `{
				"driver": "gcp.csi.confidential.cloud",
				"fsType": "ext4",
				"volumeAttributes": {
					"storage.kubernetes.io/csiProvisionerIdentity": "1637243273131-8081-gcp.csi.confidential.cloud"
				},
				"volumeHandle": "projects/velero-gcp/zones/us-central1-f/disks/pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			want:    "pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d",
			wantErr: false,
		},
		{
			name: "Constellation csi driver with invalid handle name",
			csiJSON: `{
				"driver": "gcp.csi.confidential.cloud",
				"fsType": "ext4",
				"volumeHandle": "pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			want:    "",
			wantErr: true,
		},
		{
			name: "unknown driver",
			csiJSON: `{
				"driver": "xxx.csi.storage.gke.io",
				"fsType": "ext4",
				"volumeHandle": "pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			want:    "",
			wantErr: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res := &unstructured.Unstructured{
				Object: map[string]interface{}{},
			}
			csi := map[string]interface{}{}
			json.Unmarshal([]byte(tt.csiJSON), &csi)
			res.Object["spec"] = map[string]interface{}{
				"csi": csi,
			}
			volumeID, err := b.GetVolumeID(res)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, volumeID)
		})
	}

}

func TestSetVolumeID(t *testing.T) {
	b := &VolumeSnapshotter{}
	var updatedPV runtime.Unstructured
	var err error

	pv := &unstructured.Unstructured{
		Object: map[string]interface{}{},
	}

	// missing spec.gcePersistentDisk -> error
	_, err = b.SetVolumeID(pv, "abc123")
	require.Error(t, err)

	// happy path
	gce := map[string]interface{}{}
	pv.Object["spec"] = map[string]interface{}{
		"gcePersistentDisk": gce,
	}
	updatedPV, err = b.SetVolumeID(pv, "123abc")
	require.NoError(t, err)

	res := new(v1.PersistentVolume)
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(updatedPV.UnstructuredContent(), res))
	require.NotNil(t, res.Spec.GCEPersistentDisk)
	assert.Equal(t, "123abc", res.Spec.GCEPersistentDisk.PDName)
}

func TestSetVolumeIDForCSI(t *testing.T) {
	cases := []struct {
		name           string
		csiJSON        string
		volumeID       string
		wantErr        bool
		volumeProject  string
		wantedVolumeID string
	}{
		{
			name: "set ID to CSI with GKE pd CSI driver",
			csiJSON: `{
				 "driver": "pd.csi.storage.gke.io",
				 "fsType": "ext4",
				 "volumeHandle": "projects/velero-gcp/zones/us-central1-f/disks/pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			volumeID:       "restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
			wantErr:        false,
			volumeProject:  "velero-gcp",
			wantedVolumeID: "projects/velero-gcp/zones/us-central1-f/disks/restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
		},
		{
			name: "set ID to CSI with GKE pd CSI driver, but the volumeHandle is invalid",
			csiJSON: `{
				 "driver": "pd.csi.storage.gke.io",
				 "fsType": "ext4",
				 "volumeHandle": "pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			volumeID:      "restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
			wantErr:       true,
			volumeProject: "velero-gcp",
		},
		{
			name: "set ID to CSI with Constellation pd CSI driver",
			csiJSON: `{
				 "driver": "gcp.csi.confidential.cloud",
				 "fsType": "ext4",
				 "volumeHandle": "projects/velero-gcp/zones/us-central1-f/disks/pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			volumeID:       "restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
			wantErr:        false,
			volumeProject:  "velero-gcp",
			wantedVolumeID: "projects/velero-gcp/zones/us-central1-f/disks/restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
		},
		{
			name: "set ID to CSI with Constellation pd CSI driver, but the volumeHandle is invalid",
			csiJSON: `{
				 "driver": "gcp.csi.confidential.cloud",
				 "fsType": "ext4",
				 "volumeHandle": "pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			volumeID:      "restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
			wantErr:       true,
			volumeProject: "velero-gcp",
		},
		{
			name: "set ID to CSI with unknown driver",
			csiJSON: `{
				 "driver": "xxx.csi.storage.gke.io",
				 "fsType": "ext4",
				 "volumeHandle": "projects/velero-gcp/zones/us-central1-f/disks/pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			volumeID:      "restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
			wantErr:       true,
			volumeProject: "velero-gcp",
		},
		{
			name: "volume project is different from original handle project",
			csiJSON: `{
				 "driver": "pd.csi.storage.gke.io",
				 "fsType": "ext4",
				 "volumeHandle": "projects/velero-gcp/zones/us-central1-f/disks/pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d"
			}`,
			volumeID:       "restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
			wantErr:        false,
			volumeProject:  "velero-gcp-2",
			wantedVolumeID: "projects/velero-gcp-2/zones/us-central1-f/disks/restore-fd9729b5-868b-4544-9568-1c5d9121dabc",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b := &VolumeSnapshotter{
				log:           logrus.New(),
				volumeProject: tt.volumeProject,
			}

			res := &unstructured.Unstructured{
				Object: map[string]interface{}{},
			}
			csi := map[string]interface{}{}
			json.Unmarshal([]byte(tt.csiJSON), &csi)
			res.Object["spec"] = map[string]interface{}{
				"csi": csi,
			}
			newRes, err := b.SetVolumeID(res, tt.volumeID)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				newPV := new(v1.PersistentVolume)
				require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(newRes.UnstructuredContent(), newPV))
				if tt.wantedVolumeID != "" {
					require.Equal(t, tt.wantedVolumeID, newPV.Spec.CSI.VolumeHandle)
				}
			}
		})
	}
}

func TestGetSnapshotTags(t *testing.T) {
	tests := []struct {
		name            string
		veleroTags      map[string]string
		diskDescription string
		expected        string
	}{
		{
			name:            "degenerate case (no tags)",
			veleroTags:      nil,
			diskDescription: "",
			expected:        "",
		},
		{
			name: "velero tags only get applied",
			veleroTags: map[string]string{
				"velero-key1": "velero-val1",
				"velero-key2": "velero-val2",
			},
			diskDescription: "",
			expected:        `{"velero-key1":"velero-val1","velero-key2":"velero-val2"}`,
		},
		{
			name:            "disk tags only get applied",
			veleroTags:      nil,
			diskDescription: `{"gcp-key1":"gcp-val1","gcp-key2":"gcp-val2"}`,
			expected:        `{"gcp-key1":"gcp-val1","gcp-key2":"gcp-val2"}`,
		},
		{
			name:            "non-overlapping velero and disk tags both get applied",
			veleroTags:      map[string]string{"velero-key": "velero-val"},
			diskDescription: `{"gcp-key":"gcp-val"}`,
			expected:        `{"velero-key":"velero-val","gcp-key":"gcp-val"}`,
		},
		{
			name: "when tags overlap, velero tags take precedence",
			veleroTags: map[string]string{
				"velero-key":      "velero-val",
				"overlapping-key": "velero-val",
			},
			diskDescription: `{"gcp-key":"gcp-val","overlapping-key":"gcp-val"}`,
			expected:        `{"velero-key":"velero-val","gcp-key":"gcp-val","overlapping-key":"velero-val"}`,
		},
		{
			name:            "if disk description is invalid JSON, apply just velero tags",
			veleroTags:      map[string]string{"velero-key": "velero-val"},
			diskDescription: `THIS IS INVALID JSON`,
			expected:        `{"velero-key":"velero-val"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res := getSnapshotTags(test.veleroTags, test.diskDescription, velerotest.NewLogger())

			if test.expected == "" {
				assert.Equal(t, test.expected, res)
				return
			}

			var actualMap map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(res), &actualMap))

			var expectedMap map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(test.expected), &expectedMap))

			assert.Equal(t, len(expectedMap), len(actualMap))
			for k, v := range expectedMap {
				assert.Equal(t, v, actualMap[k])
			}
		})
	}
}

func TestRegionHelpers(t *testing.T) {
	tests := []struct {
		name                string
		volumeAZ            string
		expectedRegion      string
		expectedIsMultiZone bool
		expectedError       error
	}{
		{
			name:                "valid multizone(2) tag",
			volumeAZ:            "us-central1-a__us-central1-b",
			expectedRegion:      "us-central1",
			expectedIsMultiZone: true,
			expectedError:       nil,
		},
		{
			name:                "valid multizone(4) tag",
			volumeAZ:            "us-central1-a__us-central1-b__us-central1-f__us-central1-e",
			expectedRegion:      "us-central1",
			expectedIsMultiZone: true,
			expectedError:       nil,
		},
		{
			name:                "valid single zone tag",
			volumeAZ:            "us-central1-a",
			expectedRegion:      "us-central1",
			expectedIsMultiZone: false,
			expectedError:       nil,
		},
		{
			name:                "invalid single zone tag",
			volumeAZ:            "us^central1^a",
			expectedRegion:      "",
			expectedIsMultiZone: false,
			expectedError:       errors.Errorf("failed to parse region from zone: %q", "us^central1^a"),
		},
		{
			name:                "invalid multizone tag",
			volumeAZ:            "us^central1^a__us^central1^b",
			expectedRegion:      "",
			expectedIsMultiZone: true,
			expectedError:       errors.Errorf("failed to parse region from zone: %q", "us^central1^a__us^central1^b"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expectedIsMultiZone, isMultiZone(test.volumeAZ))
			region, err := parseRegion(test.volumeAZ)
			if test.expectedError == nil {
				assert.NoError(t, err)
			} else {
				assert.Equal(t, test.expectedError.Error(), err.Error())
			}
			assert.Equal(t, test.expectedRegion, region)
		})
	}
}

func TestInit(t *testing.T) {
	credential_file_name := "./credential_file"
	default_credential_file_name := "./default_credential"
	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", default_credential_file_name)
	credential_content := `{"type": "service_account","project_id": "project-a","private_key_id":"id","private_key":"key","client_email":"a@b.com","client_id":"id","auth_uri":"uri","token_uri":"uri","auth_provider_x509_cert_url":"url","client_x509_cert_url":"url"}`
	f, err := os.Create(credential_file_name)
	require.NoError(t, err)
	_, err = f.Write([]byte(credential_content))
	require.NoError(t, err)

	f, err = os.Create(default_credential_file_name)
	require.NoError(t, err)
	_, err = f.Write([]byte(credential_content))
	require.NoError(t, err)

	tests := []struct {
		name                      string
		config                    map[string]string
		expectedVolumeSnapshotter VolumeSnapshotter
	}{
		{
			name: "Init with Credential files.",
			config: map[string]string{
				"project":          "project-a",
				"credentialsFile":  credential_file_name,
				"snapshotLocation": "default",
				"volumeProject":    "project-b",
			},
			expectedVolumeSnapshotter: VolumeSnapshotter{
				snapshotLocation: "default",
				volumeProject:    "project-b",
				snapshotProject:  "project-a",
				snapshotType:     "STANDARD",
			},
		},
		{
			name: "Init without Credential files.",
			config: map[string]string{
				"project":          "project-a",
				"snapshotLocation": "default",
				"snapshotType":     "standard",
				"volumeProject":    "project-b",
			},
			expectedVolumeSnapshotter: VolumeSnapshotter{
				snapshotLocation: "default",
				volumeProject:    "project-b",
				snapshotProject:  "project-a",
				snapshotType:     "STANDARD",
			},
		},
		{
			name: "Init with archive snapshot type and custom universe domain.",
			config: map[string]string{
				"project":          "project-a",
				"snapshotLocation": "default",
				"snapshotType":     "archive",
				"volumeProject":    "project-b",
				"universeDomain": 	"googleapis-ppd.com",
			},
			expectedVolumeSnapshotter: VolumeSnapshotter{
				snapshotLocation: "default",
				volumeProject:    "project-b",
				snapshotProject:  "project-a",
				snapshotType:     "ARCHIVE",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			volumeSnapshotter := newVolumeSnapshotter(logrus.StandardLogger())
			err := volumeSnapshotter.Init(test.config)
			require.NoError(t, err)
			require.Equal(t, test.expectedVolumeSnapshotter.snapshotLocation, volumeSnapshotter.snapshotLocation)
			require.Equal(t, test.expectedVolumeSnapshotter.volumeProject, volumeSnapshotter.volumeProject)
			require.Equal(t, test.expectedVolumeSnapshotter.snapshotProject, volumeSnapshotter.snapshotProject)
			require.Equal(t, test.expectedVolumeSnapshotter.snapshotType, volumeSnapshotter.snapshotType)
		})
	}

	err = os.Remove(credential_file_name)
	require.NoError(t, err)
	err = os.Remove(default_credential_file_name)
	require.NoError(t, err)
}

func TestIsVolumeCreatedCrossProjects(t *testing.T) {
	tests := []struct {
		name              string
		volumeSnapshotter VolumeSnapshotter
		volumeHandle      string
		expectedResult    bool
	}{
		{
			name: "Invalid Volume handle",
			volumeSnapshotter: VolumeSnapshotter{
				log: logrus.New(),
			},
			volumeHandle:   "InvalidHandle",
			expectedResult: false,
		},
		{
			name: "Volume is created cross-project",
			volumeSnapshotter: VolumeSnapshotter{
				log:           logrus.New(),
				volumeProject: "velero-gcp-2",
			},
			volumeHandle:   "projects/velero-gcp/zones/us-central1-f/disks/pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d",
			expectedResult: true,
		},
		{
			name: "Volume is not created cross-project",
			volumeSnapshotter: VolumeSnapshotter{
				log:           logrus.New(),
				volumeProject: "velero-gcp",
			},
			volumeHandle:   "projects/velero-gcp/zones/us-central1-f/disks/pvc-a970184f-6cc1-4769-85ad-61dcaf8bf51d",
			expectedResult: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expectedResult, test.volumeSnapshotter.IsVolumeCreatedCrossProjects(test.volumeHandle))
		})
	}
}
