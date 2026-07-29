/*
Copyright 2017, 2019 the Velero contributors.

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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	uuid "github.com/gofrs/uuid"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	veleroplugin "github.com/vmware-tanzu/velero/pkg/plugin/framework"
)

const (
	zoneSeparator       		= "__"
	projectKey          		= "project"
	snapshotLocationKey 		= "snapshotLocation"
	snapshotTypeKey     		= "snapshotType"
	volumeProjectKey    		= "volumeProject"
	snapshotUniverseDomainKey   = "universeDomain"
)

var pdCSIDriver = map[string]bool{
	"pd.csi.storage.gke.io":      true,
	"gcp.csi.confidential.cloud": true,
}

var pdVolRegexp = regexp.MustCompile(`^projects\/[^\/]+\/(zones|regions)\/[^\/]+\/disks\/[^\/]+$`)

type VolumeSnapshotter struct {
	log              logrus.FieldLogger
	gce              *compute.Service
	snapshotLocation string
	volumeProject    string
	snapshotProject  string
	snapshotType     string
}

func newVolumeSnapshotter(logger logrus.FieldLogger) *VolumeSnapshotter {
	return &VolumeSnapshotter{log: logger}
}

func (b *VolumeSnapshotter) Init(config map[string]string) error {
	if err := veleroplugin.ValidateVolumeSnapshotterConfigKeys(
		config,
		snapshotLocationKey,
		snapshotTypeKey,
		projectKey,
		credentialsFileConfigKey,
		volumeProjectKey,
		snapshotUniverseDomainKey,
	); err != nil {
		return err
	}

	clientOptions := []option.ClientOption{
		option.WithScopes(compute.ComputeScope),
	}

	// Credentials used to connect to GCP compute service.
	var creds *google.Credentials
	var err error

	// If credential is provided for the VSL, use it instead of default credential.
	if credentialsFile, ok := config[credentialsFileConfigKey]; ok {
		b, err := os.ReadFile(credentialsFile)
		if err != nil {
			return errors.Wrapf(err, "error reading provided credentials file %v", credentialsFile)
		}

		creds, err = google.CredentialsFromJSON(context.TODO(), b)
		if err != nil {
			return errors.WithStack(err)
		}

		// If using a credentials file, we also need to pass it when creating the client.
		clientOptions = append(clientOptions, option.WithCredentialsFile(credentialsFile))
	} else {
		/* Use default credential, when no credential is provisioned in VSL. */
		creds, err = google.FindDefaultCredentials(context.TODO(), compute.ComputeScope)
		if err != nil {
			return errors.WithStack(err)
		}
		clientOptions = append(clientOptions, option.WithTokenSource(creds.TokenSource))
	}

	b.snapshotLocation = config[snapshotLocationKey]

	b.volumeProject = config[volumeProjectKey]
	if b.volumeProject == "" {
		b.volumeProject = creds.ProjectID
	}

	// get snapshot project from 'project' config key if specified,
	// otherwise from the credentials file
	b.snapshotProject = config[projectKey]
	if b.snapshotProject == "" {
		b.snapshotProject = b.volumeProject
	}

	// get snapshot type from 'snapshotType' config key if specified,
	// otherwise default to "STANDARD"
	snapshotType := strings.ToUpper(config[snapshotTypeKey])
	switch snapshotType {
	case "":
		b.snapshotType = "STANDARD"
	case "STANDARD", "ARCHIVE":
		b.snapshotType = snapshotType
	default:
		return errors.Errorf("unsupported snapshot type: %q", snapshotType)
	}

	// if universeDomain is provided, we need to pass it when creating new compute service client.
	if snapshotUniverseDomain, ok := config[snapshotUniverseDomainKey]; ok {
		clientOptions = append(clientOptions, option.WithUniverseDomain(snapshotUniverseDomain))
	}

	gce, err := compute.NewService(context.TODO(), clientOptions...)
	if err != nil {
		return errors.WithStack(err)
	}

	b.gce = gce

	return nil
}

// isMultiZone returns true if the failure-domain tag contains
// double underscore, which is the separator used
// by GKE when a storage class spans multiple availability
// zones.
func isMultiZone(volumeAZ string) bool {
	return strings.Contains(volumeAZ, zoneSeparator)
}

// parseRegion parses a failure-domain tag with multiple zones
// and returns a single region. Zones are separated by double underscores (__).
// For example
//
//	input: us-central1-a__us-central1-b
//	return: us-central1
//
// When a custom storage class spans multiple geographical zones,
// such as us-central1 and us-west1 only the zone matching the cluster is used
// in the failure-domain tag.
// For example
//
//	Cluster nodes in us-central1-c, us-central1-f
//	Storage class zones us-central1-a, us-central1-f, us-east1-a, us-east1-d
//	The failure-domain tag would be: us-central1-a__us-central1-f
func parseRegion(volumeAZ string) (string, error) {
	zones := strings.Split(volumeAZ, zoneSeparator)
	zone := zones[0]
	parts := strings.SplitAfterN(zone, "-", 3)
	if len(parts) < 2 {
		return "", errors.Errorf("failed to parse region from zone: %q", volumeAZ)
	}
	return parts[0] + strings.TrimSuffix(parts[1], "-"), nil
}

// Retrieve the URLs for zones via the GCP API.
func (b *VolumeSnapshotter) getZoneURLs(volumeAZ string) ([]string, error) {
	zones := strings.Split(volumeAZ, zoneSeparator)
	var zoneURLs []string
	for _, z := range zones {
		zone, err := b.gce.Zones.Get(b.volumeProject, z).Do()
		if err != nil {
			return nil, errors.WithStack(err)
		}

		zoneURLs = append(zoneURLs, zone.SelfLink)
	}

	return zoneURLs, nil
}

func (b *VolumeSnapshotter) CreateVolumeFromSnapshot(snapshotID, volumeType, volumeAZ string, iops *int64) (volumeID string, err error) {
	// get the snapshot so we can apply its tags to the volume
	res, err := b.gce.Snapshots.Get(b.snapshotProject, snapshotID).Do()
	if err != nil {
		return "", errors.WithStack(err)
	}

	// Kubernetes uses the description field of GCP disks to store a JSON doc containing
	// tags.
	//
	// use the snapshot's description (which contains tags from the snapshotted disk
	// plus Velero-specific tags) to set the new disk's description.
	uid, err := uuid.NewV4()
	if err != nil {
		return "", errors.WithStack(err)
	}
	disk := &compute.Disk{
		Name:           "restore-" + uid.String(),
		SourceSnapshot: res.SelfLink,
		Type:           volumeType,
		Description:    res.Description,
		Labels:         res.Labels,
	}

	if isMultiZone(volumeAZ) {
		volumeRegion, err := parseRegion(volumeAZ)
		if err != nil {
			return "", err
		}

		// URLs for zones that the volume is replicated to within GCP
		zoneURLs, err := b.getZoneURLs(volumeAZ)
		if err != nil {
			return "", err
		}

		disk.ReplicaZones = zoneURLs

		if _, err = b.gce.RegionDisks.Insert(b.volumeProject, volumeRegion, disk).Do(); err != nil {
			return "", errors.WithStack(err)
		}
	} else {
		if _, err = b.gce.Disks.Insert(b.volumeProject, volumeAZ, disk).Do(); err != nil {
			return "", errors.WithStack(err)
		}
	}

	return disk.Name, nil
}

func (b *VolumeSnapshotter) GetVolumeInfo(volumeID, volumeAZ string) (string, *int64, error) {
	var (
		res *compute.Disk
		err error
	)

	if isMultiZone(volumeAZ) {
		volumeRegion, err := parseRegion(volumeAZ)
		if err != nil {
			return "", nil, errors.WithStack(err)
		}
		res, err = b.gce.RegionDisks.Get(b.volumeProject, volumeRegion, volumeID).Do()
		if err != nil {
			return "", nil, errors.WithStack(err)
		}
	} else {
		res, err = b.gce.Disks.Get(b.volumeProject, volumeAZ, volumeID).Do()
		if err != nil {
			return "", nil, errors.WithStack(err)
		}
	}
	return res.Type, nil, nil
}

func (b *VolumeSnapshotter) CreateSnapshot(volumeID, volumeAZ string, tags map[string]string) (string, error) {
	// snapshot names must adhere to RFC1035 and be 1-63 characters
	// long
	var snapshotName string
	uid, err := uuid.NewV4()
	if err != nil {
		return "", errors.WithStack(err)
	}
	suffix := "-" + uid.String()

	// List all project quotas and check the "SNAPSHOTS" quota.
	// If the limit is reached, return an error, so that snapshot
	// won't get created.
	p, err := b.gce.Projects.Get(b.volumeProject).Do()
	if err != nil {
		return "", errors.WithStack(err)
	}

	for _, quota := range p.Quotas {
		if quota.Metric == "SNAPSHOTS" {
			if quota.Usage == quota.Limit {
				err := fmt.Errorf("snapshots quota on Google Cloud Platform has been reached")
				return "", errors.WithStack(err)
			}
			break
		}
	}

	if len(volumeID) <= (63 - len(suffix)) {
		snapshotName = volumeID + suffix
	} else {
		snapshotName = volumeID[0:63-len(suffix)] + suffix
	}

	if isMultiZone(volumeAZ) {
		volumeRegion, err := parseRegion(volumeAZ)
		if err != nil {
			return "", errors.WithStack(err)
		}
		return b.createRegionSnapshot(snapshotName, volumeID, volumeRegion, tags)
	} else {
		return b.createSnapshot(snapshotName, volumeID, volumeAZ, tags)
	}
}

func (b *VolumeSnapshotter) createSnapshot(snapshotName, volumeID, volumeAZ string, tags map[string]string) (string, error) {
	disk, err := b.gce.Disks.Get(b.volumeProject, volumeAZ, volumeID).Do()
	if err != nil {
		return "", errors.WithStack(err)
	}

	snapshot := &compute.Snapshot{
		Name:         snapshotName,
		Description:  getSnapshotTags(tags, disk.Description, b.log),
		SourceDisk:   disk.SelfLink,
		SnapshotType: b.snapshotType,
		Labels:       disk.Labels,
	}

	if b.snapshotLocation != "" {
		snapshot.StorageLocations = []string{b.snapshotLocation}
	}

	// Try creating snapshot with labels
	_, err = b.gce.Snapshots.Insert(b.snapshotProject, snapshot).Do()

	// If we get a permission error for labels, retry without them
	if err != nil && isLabelPermissionError(err) {
		b.log.WithError(err).Warn("Missing compute.snapshots.setLabels permission, creating snapshot without labels")

		// Retry without labels
		snapshot.Labels = nil
		_, err = b.gce.Snapshots.Insert(b.snapshotProject, snapshot).Do()
		if err != nil {
			return "", errors.WithStack(err)
		}
	} else if err != nil {
		return "", errors.WithStack(err)
	}

	return snapshot.Name, nil
}

func (b *VolumeSnapshotter) createRegionSnapshot(snapshotName, volumeID, volumeRegion string, tags map[string]string) (string, error) {
	disk, err := b.gce.RegionDisks.Get(b.volumeProject, volumeRegion, volumeID).Do()
	if err != nil {
		return "", errors.WithStack(err)
	}

	gceSnap := compute.Snapshot{
		Name:         snapshotName,
		Description:  getSnapshotTags(tags, disk.Description, b.log),
		SourceDisk:   disk.SelfLink,
		SnapshotType: b.snapshotType,
		Labels:       disk.Labels,
	}

	if b.snapshotLocation != "" {
		gceSnap.StorageLocations = []string{b.snapshotLocation}
	}

	_, err = b.gce.Snapshots.Insert(b.snapshotProject, &gceSnap).Do()
	if err != nil {
		return "", errors.WithStack(err)
	}

	return gceSnap.Name, nil
}

func getSnapshotTags(veleroTags map[string]string, diskDescription string, log logrus.FieldLogger) string {
	// Kubernetes uses the description field of GCP disks to store a JSON doc containing
	// tags.
	//
	// use the tags in the disk's description (if a valid JSON doc) plus the tags arg
	// to set the snapshot's description.
	var snapshotTags map[string]string
	if diskDescription != "" {
		if err := json.Unmarshal([]byte(diskDescription), &snapshotTags); err != nil {
			// error decoding the disk's description, so just use the Velero-assigned tags
			log.WithField("error", err.Error()).Warning("unable to decode disk's description as JSON, so only applying Velero-assigned tags to snapshot")
			snapshotTags = veleroTags
		} else {
			// merge Velero-assigned tags with the disk's tags (note that we want current
			// Velero-assigned tags to overwrite any older versions of them that may exist
			// due to prior snapshots/restores)
			for k, v := range veleroTags {
				snapshotTags[k] = v
			}
		}
	} else {
		// no disk description provided, assign velero tags
		snapshotTags = veleroTags
	}

	if len(snapshotTags) == 0 {
		return ""
	}

	tagsJSON, err := json.Marshal(snapshotTags)
	if err != nil {
		log.WithError(err).Error("unable to encode snapshot's tags to JSON, so not tagging snapshot")
		return ""
	}

	return string(tagsJSON)
}

func (b *VolumeSnapshotter) DeleteSnapshot(snapshotID string) error {

	_, err := b.gce.Snapshots.Delete(b.snapshotProject, snapshotID).Do()

	// if it's a 404 (not found) error, we don't need to return an error
	// since the snapshot is not there.
	if gcpErr, ok := err.(*googleapi.Error); ok && gcpErr.Code == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (b *VolumeSnapshotter) GetVolumeID(unstructuredPV runtime.Unstructured) (string, error) {
	pv := new(v1.PersistentVolume)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredPV.UnstructuredContent(), pv); err != nil {
		return "", errors.WithStack(err)
	}

	if pv.Spec.CSI != nil {
		driver := pv.Spec.CSI.Driver
		if pdCSIDriver[driver] {
			handle := pv.Spec.CSI.VolumeHandle
			if !pdVolRegexp.MatchString(handle) {
				return "", fmt.Errorf("invalid volumeHandle for CSI driver:%s, expected projects/{project}/zones/{zone}/disks/{name}, got %s",
					driver, handle)
			}
			l := strings.Split(handle, "/")
			return l[len(l)-1], nil
		}
		b.log.Infof("Unable to handle CSI driver: %s", driver)
	}

	if pv.Spec.GCEPersistentDisk != nil {
		if pv.Spec.GCEPersistentDisk.PDName == "" {
			return "", errors.New("spec.gcePersistentDisk.pdName not found")
		}
		return pv.Spec.GCEPersistentDisk.PDName, nil
	}

	return "", nil
}

func (b *VolumeSnapshotter) SetVolumeID(unstructuredPV runtime.Unstructured, volumeID string) (runtime.Unstructured, error) {
	pv := new(v1.PersistentVolume)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredPV.UnstructuredContent(), pv); err != nil {
		return nil, errors.WithStack(err)
	}
	if pv.Spec.CSI != nil {
		// PV is provisioned by CSI driver
		driver := pv.Spec.CSI.Driver
		if pdCSIDriver[driver] {
			handle := pv.Spec.CSI.VolumeHandle
			// To restore in the same AZ, here we only replace the 'disk' chunk.
			if !pdVolRegexp.MatchString(handle) {
				return nil, fmt.Errorf("invalid volumeHandle for restore with CSI driver:%s, expected projects/{project}/zones/{zone}/disks/{name}, got %s",
					driver, handle)
			}
			if b.IsVolumeCreatedCrossProjects(handle) == true {
				projectRE := regexp.MustCompile(`projects\/[^\/]+\/`)
				handle = projectRE.ReplaceAllString(handle, "projects/"+b.volumeProject+"/")
			}
			pv.Spec.CSI.VolumeHandle = handle[:strings.LastIndex(handle, "/")+1] + volumeID
		} else {
			return nil, fmt.Errorf("unable to handle CSI driver: %s", driver)
		}
	} else if pv.Spec.GCEPersistentDisk != nil {
		// PV is provisioned by in-tree driver
		pv.Spec.GCEPersistentDisk.PDName = volumeID
	} else {
		return nil, errors.New("spec.csi and spec.gcePersistentDisk not found")
	}
	res, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pv)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &unstructured.Unstructured{Object: res}, nil
}

func (b *VolumeSnapshotter) IsVolumeCreatedCrossProjects(volumeHandle string) bool {
	// Get project ID from volume handle
	parsedStr := strings.Split(volumeHandle, "/")
	if len(parsedStr) < 2 {
		return false
	}
	projectID := parsedStr[1]

	if projectID != b.volumeProject {
		return true
	}

	return false
}

// isLabelPermissionError Helper function to detect label permission errors
func isLabelPermissionError(err error) bool {
	if err == nil {
		return false
	}

	// Check for specific GCP permission error
	// This might need adjustment based on actual error format
	errStr := err.Error()
	return strings.Contains(errStr, "compute.snapshots.setLabels") ||
		(strings.Contains(errStr, "permission") && strings.Contains(errStr, "label"))
}
