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

package main

import (
	"os"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func zonalPV(t *testing.T, spec v1.PersistentVolumeSource) *unstructured.Unstructured {
	t.Helper()
	pv := &v1.PersistentVolume{}
	pv.Labels = map[string]string{
		"topology.kubernetes.io/zone":              "europe-west4-c",
		"topology.kubernetes.io/region":            "europe-west4",
		"failure-domain.beta.kubernetes.io/zone":   "europe-west4-c",
		"failure-domain.beta.kubernetes.io/region": "europe-west4",
		"app": "mongodb",
	}
	pv.Spec.PersistentVolumeSource = spec
	pv.Spec.NodeAffinity = &v1.VolumeNodeAffinity{Required: &v1.NodeSelector{NodeSelectorTerms: []v1.NodeSelectorTerm{{
		MatchExpressions: []v1.NodeSelectorRequirement{
			{Key: "failure-domain.beta.kubernetes.io/zone", Operator: v1.NodeSelectorOpIn, Values: []string{"europe-west4-c"}},
			{Key: "topology.kubernetes.io/region", Operator: v1.NodeSelectorOpIn, Values: []string{"europe-west4"}},
			{Key: "topology.gke.io/zone", Operator: v1.NodeSelectorOpIn, Values: []string{"europe-west4-c"}},
			{Key: "cloud.google.com/gke-nodepool", Operator: v1.NodeSelectorOpIn, Values: []string{"persistent"}},
		},
	}}}}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pv)
	require.NoError(t, err)
	return &unstructured.Unstructured{Object: obj}
}

func toPV(t *testing.T, res runtime.Unstructured) *v1.PersistentVolume {
	t.Helper()
	pv := &v1.PersistentVolume{}
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(res.UnstructuredContent(), pv))
	return pv
}

func requireTopology(t *testing.T, pv *v1.PersistentVolume, zone, region string) {
	t.Helper()
	require.Equal(t, zone, pv.Labels["topology.kubernetes.io/zone"])
	require.Equal(t, region, pv.Labels["topology.kubernetes.io/region"])
	require.Equal(t, zone, pv.Labels["failure-domain.beta.kubernetes.io/zone"])
	require.Equal(t, region, pv.Labels["failure-domain.beta.kubernetes.io/region"])
	require.Equal(t, "mongodb", pv.Labels["app"])
	exprs := pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions
	require.Equal(t, []string{zone}, exprs[0].Values)
	require.Equal(t, []string{region}, exprs[1].Values)
	require.Equal(t, []string{zone}, exprs[2].Values)
	require.Equal(t, []string{"persistent"}, exprs[3].Values)
}

func TestRestoreZone(t *testing.T) {
	require.Equal(t, "europe-west4-c", (&VolumeSnapshotter{}).restoreZone("europe-west4-c"))
	require.Equal(t, "europe-west4-a__europe-west4-c", (&VolumeSnapshotter{}).restoreZone("europe-west4-a__europe-west4-c"))
	require.Equal(t, "europe-west2-b", (&VolumeSnapshotter{volumeZone: "europe-west2-b"}).restoreZone("europe-west4-c"))
	require.Equal(t, "europe-west2-b", (&VolumeSnapshotter{volumeZone: "europe-west2-b"}).restoreZone("europe-west4-a__europe-west4-c"))
}

func TestSetVolumeIDWithVolumeZone(t *testing.T) {
	cases := []struct {
		name       string
		handle     string
		wantHandle string
	}{
		{
			name:       "zonal CSI disk moves zone and project",
			handle:     "projects/mip-prod/zones/europe-west4-c/disks/pvc-1",
			wantHandle: "projects/mip-drp/zones/europe-west2-b/disks/restore-1",
		},
		{
			name:       "regional CSI disk becomes a zonal disk in volumeZone",
			handle:     "projects/mip-prod/regions/europe-west4/disks/pvc-2",
			wantHandle: "projects/mip-drp/zones/europe-west2-b/disks/restore-1",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b := &VolumeSnapshotter{log: logrus.New(), volumeProject: "mip-drp", volumeZone: "europe-west2-b"}
			res, err := b.SetVolumeID(zonalPV(t, v1.PersistentVolumeSource{CSI: &v1.CSIPersistentVolumeSource{
				Driver: "pd.csi.storage.gke.io", VolumeHandle: tt.handle,
			}}), "restore-1")
			require.NoError(t, err)
			pv := toPV(t, res)
			require.Equal(t, tt.wantHandle, pv.Spec.CSI.VolumeHandle)
			requireTopology(t, pv, "europe-west2-b", "europe-west2")
		})
	}

	t.Run("in-tree GCE PD gets new name, zone and region", func(t *testing.T) {
		b := &VolumeSnapshotter{log: logrus.New(), volumeProject: "mip-drp", volumeZone: "europe-west2-b"}
		res, err := b.SetVolumeID(zonalPV(t, v1.PersistentVolumeSource{GCEPersistentDisk: &v1.GCEPersistentDiskVolumeSource{
			PDName: "gke-old-disk",
		}}), "restore-1")
		require.NoError(t, err)
		pv := toPV(t, res)
		require.Equal(t, "restore-1", pv.Spec.GCEPersistentDisk.PDName)
		requireTopology(t, pv, "europe-west2-b", "europe-west2")
	})

	t.Run("without volumeZone nothing but the disk name changes", func(t *testing.T) {
		b := &VolumeSnapshotter{log: logrus.New(), volumeProject: "mip-prod"}
		res, err := b.SetVolumeID(zonalPV(t, v1.PersistentVolumeSource{CSI: &v1.CSIPersistentVolumeSource{
			Driver: "pd.csi.storage.gke.io", VolumeHandle: "projects/mip-prod/zones/europe-west4-c/disks/pvc-1",
		}}), "restore-1")
		require.NoError(t, err)
		pv := toPV(t, res)
		require.Equal(t, "projects/mip-prod/zones/europe-west4-c/disks/restore-1", pv.Spec.CSI.VolumeHandle)
		requireTopology(t, pv, "europe-west4-c", "europe-west4")
	})

	t.Run("PV without node affinity or labels", func(t *testing.T) {
		b := &VolumeSnapshotter{log: logrus.New(), volumeProject: "mip-drp", volumeZone: "europe-west2-b"}
		pv := &v1.PersistentVolume{Spec: v1.PersistentVolumeSpec{PersistentVolumeSource: v1.PersistentVolumeSource{
			CSI: &v1.CSIPersistentVolumeSource{Driver: "pd.csi.storage.gke.io", VolumeHandle: "projects/mip-prod/zones/europe-west4-c/disks/pvc-1"},
		}}}
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pv)
		require.NoError(t, err)
		res, err := b.SetVolumeID(&unstructured.Unstructured{Object: obj}, "restore-1")
		require.NoError(t, err)
		require.Equal(t, "projects/mip-drp/zones/europe-west2-b/disks/restore-1", toPV(t, res).Spec.CSI.VolumeHandle)
	})
}

func TestInitVolumeZone(t *testing.T) {
	credentials := "./volume_zone_credentials"
	require.NoError(t, os.WriteFile(credentials, []byte(`{"type": "service_account","project_id": "project-a","private_key_id":"id","private_key":"key","client_email":"a@b.com","client_id":"id","auth_uri":"uri","token_uri":"uri","auth_provider_x509_cert_url":"url","client_x509_cert_url":"url"}`), 0o600))
	defer os.Remove(credentials)

	b := newVolumeSnapshotter(logrus.New())
	require.NoError(t, b.Init(map[string]string{"credentialsFile": credentials, "volumeZone": "europe-west2-b"}))
	require.Equal(t, "europe-west2-b", b.volumeZone)

	for _, zone := range []string{"europe-west2", "europe-west2-b__europe-west2-c", "Europe-West2-B", "europe-west2-b "} {
		t.Run(zone, func(t *testing.T) {
			require.Error(t, newVolumeSnapshotter(logrus.New()).Init(map[string]string{"credentialsFile": credentials, "volumeZone": zone}))
		})
	}

	b = newVolumeSnapshotter(logrus.New())
	require.NoError(t, b.Init(map[string]string{"credentialsFile": credentials}))
	require.Equal(t, "", b.volumeZone)
}
