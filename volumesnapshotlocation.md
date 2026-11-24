# Volume Snapshot Location

The following sample GCP `VolumeSnapshotLocation` YAML shows all of the configurable parameters. The items under `spec.config` can be provided as key-value pairs to the `velero install` command's `--snapshot-location-config` flag -- for example, `snapshotLocation=us-central1,project=my-project,...`.

```yaml
apiVersion: velero.io/v1
kind: VolumeSnapshotLocation
metadata:
  name: gcp-default
  namespace: velero
spec:
  # Name of the volume snapshotter plugin to use to connect to this location.
  #
  # Required.
  provider: velero.io/gcp
  
  config:
    # The GCP location where snapshots should be stored. See the GCP documentation
    # (https://cloud.google.com/storage/docs/locations#available_locations) for the
    # full list. If not specified, snapshots are stored in the default location
    # (https://cloud.google.com/compute/docs/disks/create-snapshots#default_location).
    #
    # Optional.
    snapshotLocation: us-central1

    # The project ID where existing snapshots should be retrieved from during restores, if 
    # different than the project that your IAM account is in. This field has no effect on 
    # where new snapshots are created; it is only useful for restoring existing snapshots 
    # from a different project.
    # 
    # Optional (defaults to the project that the GCP IAM account is in).
    project: my-alternate-project

    # The preferred credentials to talk to the GCP snapshot service.
    #
    # Optional.
    credentialsFile: path/to/my/credential

    # The project to manipulate volumes. 
    # This is useful the backup and restore happens in different projects.
    #
    # Optional (default to be same the credential's project).
    volumeProject: project-id

    # The zone that restored disks are created in, instead of the zone recorded in
    # the backup. Use it to restore into a cluster in another zone or region, for
    # example in a disaster recovery. The plugin also points the zone and region in
    # the restored PV's volume handle, node affinity and topology labels at this
    # zone. Regional disks are restored as zonal disks in this zone.
    # Snapshots must be stored in a location that covers the target zone, such as
    # a multi-region.
    #
    # Optional (default: the zone recorded in the backup).
    volumeZone: europe-west2-b

    # The type of the created snapshot. Two types are supported: STANDARD and ARCHIVE.
    #
    # Optional (default to STANDARD).
    snapshotType: snapshot-type
```
