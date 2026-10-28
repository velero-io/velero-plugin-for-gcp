# Backup Storage Location

The following sample GCP `BackupStorageLocation` YAML shows all of the configurable parameters. The items under `spec.config` can be provided as key-value pairs to the `velero install` command's `--backup-location-config` flag -- for example, `kmsKeyName=my-kms-key,serviceAccount=my-service-account,...`.

```yaml
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: default
  namespace: velero
spec:
  # Name of the object store plugin to use to connect to this location.
  #
  # Required.
  provider: velero.io/gcp

  objectStorage:
    # The bucket in which to store backups.
    #
    # Required.
    bucket: my-bucket

    # The prefix within the bucket under which to store backups.
    #
    # Optional.
    prefix: my-prefix

  config:
    # Name of the Cloud KMS key to use to encrypt backups stored in this location, in the form
    # "projects/P/locations/L/keyRings/R/cryptoKeys/K". See customer-managed Cloud KMS keys
    # (https://cloud.google.com/storage/docs/encryption/using-customer-managed-keys) for details.
    #
    # Optional.
    kmsKeyName: projects/my-project/locations/my-location/keyRings/my-keyring/cryptoKeys/my-key

    # Name of the GCP service account to use for this backup storage location. Specify the
    # service account here if you want to use workload identity instead of providing the key file.
    # It is also required when using Workforce/Workload Identity Federation (external_account
    # credentials) so that the plugin can impersonate this service account to generate signed URLs.
    #
    # Optional (defaults to "false").
    serviceAccount: my-service-account

    # The preferred credentials to talk to the GCP cloud storage service.
    #
    # Optional.
    credentialsFile: path/to/my/credential

    # Configuration of storage endpoint for GCS bucket
    #
    # Optional.
    storeEndpoint: storage-example.p.googleapis.com

    # Configuration of the universe domain
    #
    # Optional.
    universeDomain: googleapis.com
```
