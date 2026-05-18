[![Build Status][101]][102]

# Plugins for Google Cloud Platform (GCP)

## Overview

This repository contains these plugins to support running Velero on GCP:

- An object store plugin for persisting and retrieving backups and restores on Google Cloud Storage. Content of backup includes log files, warning/error files, CSI related resources list, Velero native snapshots list, Velero PodVolumeBackup list, k8s resources list, k8s resources YAMLs and files created by uploader (Restic and Kopia). Content of restore includes restore logs, warning/error files and coming k8s resources list.

- A volume snapshotter plugin for creating snapshots from volumes (during a backup) and volumes from snapshots (during a restore) on Google Compute Engine Disks.

  - Since v1.4.0, the snapshotter plugin can handle the volumes provisioned by CSI driver `pd.csi.storage.gke.io`.

You can run Kubernetes on Google Cloud Platform in either:

* Kubernetes on Google Compute Engine virtual machines
* Google Kubernetes Engine

For common use-cases, take a look at the [Examples][10] page.


## Compatibility

Below is a listing of plugin versions and respective Velero versions that are compatible.

| Plugin Version  | Velero Version |
|-----------------|----------------|
| v1.13.x         | v1.17.x        |
| v1.12.x         | v1.16.x        |
| v1.11.x         | v1.15.x        |
| v1.10.x         | v1.14.x        |
| v1.9.x          | v1.13.x        |

## Filing issues

If you would like to file a GitHub issue for the plugin, please open the issue on the [core Velero repo][103]

## Setup

To set up Velero on GCP, you:

* [Create an GCS bucket][1]
* [Set permissions for Velero][2]
* [Install and start Velero][3]

You can also use this plugin to create an additional [Backup Storage Location][12].

If you do not have the `gcloud` and `gsutil` CLIs locally installed, follow the [user guide][5] to set them up.

## Create an GCS bucket

Velero requires an object storage bucket in which to store backups, preferably unique to a single Kubernetes cluster (see the [FAQ][11] for more details). Create a GCS bucket, replacing the <YOUR_BUCKET> placeholder with the name of your bucket:

```bash
BUCKET=<YOUR_BUCKET>

gsutil mb gs://$BUCKET/
```

## Set permissions for Velero

If you run Google Kubernetes Engine (GKE), make sure that your current IAM user is a cluster-admin. This role is required to create RBAC objects.
See [the GKE documentation][22] for more information.

### Create Google Service Account (GSA):
To integrate Velero with GCP, create a Velero-specific [Service Account][21]:

1. View your current config settings:

    ```bash
    gcloud config list
    ```

    Store the `project` value from the results in the environment variable `$PROJECT_ID`.

    ```bash
    PROJECT_ID=$(gcloud config get-value project)
    ```

2. Create a service account:

    ```bash
    GSA_NAME=velero
    gcloud iam service-accounts create $GSA_NAME \
        --display-name "Velero service account"
    ```

    > If you'll be using Velero to backup multiple clusters with multiple GCS buckets, it may be desirable to create a unique username per cluster rather than the default `velero`.

    Then list all accounts and find the `velero` account you just created:

    ```bash
    gcloud iam service-accounts list
    ```

    Set the `$SERVICE_ACCOUNT_EMAIL` variable to match its `email` value.

    ```bash
    SERVICE_ACCOUNT_EMAIL=$(gcloud iam service-accounts list \
      --filter="displayName:Velero service account" \
      --format 'value(email)')
    ```

### Create Custom Role with Permissions for the Velero GSA:
These permissions are required by Velero to manage snapshot resources in the GCP Project.
    
    ```bash
    ROLE_PERMISSIONS=(
        compute.disks.get
        compute.disks.create
        compute.disks.createSnapshot
        compute.disks.setLabels
        compute.projects.get
        compute.snapshots.get
        compute.snapshots.create
        compute.snapshots.useReadOnly
        compute.snapshots.delete
        compute.snapshots.setLabels
        compute.zones.get
        storage.objects.create
        storage.objects.delete
        storage.objects.get
        storage.objects.list
        iam.serviceAccounts.signBlob
    )
    
    gcloud iam roles create velero.server \
        --project $PROJECT_ID \
        --title "Velero Server" \
        --permissions "$(IFS=","; echo "${ROLE_PERMISSIONS[*]}")"

    gcloud projects add-iam-policy-binding $PROJECT_ID \
        --member serviceAccount:$SERVICE_ACCOUNT_EMAIL \
        --role projects/$PROJECT_ID/roles/velero.server

    gsutil iam ch serviceAccount:$SERVICE_ACCOUNT_EMAIL:objectAdmin gs://${BUCKET}
    ```

Note: 
`iam.serviceAccounts.signBlob` permission is used to allow [Velero's Kubernetes Service Account](#Option-2:-Using-Workload-Identity) to create signed urls for the GCS bucket.
This is required if you want to run `velero backup logs`, `velero backup download`, `velero backup describe` and `velero restore describe`.
This is due to those commands need to download some metadata files from S3 bucket to display information needed, and the Velero server has access to GCS but the CLI does not.

### Grant access to Velero 
This can be done in 2 different options.

#### Option 1: Using Service Account Key
This involves creating a Google Service Account Key and using it as `--secret-file` during [installation](#Install-and-start-Velero).

1. Create a service account key, specifying an output file (`credentials-velero`) in your local directory:

    ```bash
    gcloud iam service-accounts keys create credentials-velero \
        --iam-account $SERVICE_ACCOUNT_EMAIL
    ```

Note that Google Service Account keys are valid for decades (no clear expiry date) - so store it securely or rotate them as often as possible or both. 

#### Option 2: Using File-sourced workforce identity federation short lived credentials

Keep in mind that [Workforce Identity Federation Users cannot generate signed URLs](https://cloud.google.com/iam/docs/federated-identity-supported-services#:~:text=workforce%20identity%20federation%20users%20cannot%20generate%20signed%20URLs.). This means, if you are using Workforce Identity Federation, you will not be able to run `velero backup logs`, `velero backup download`, `velero backup describe` and `velero restore describe`.

This involves creating an external credential file and using it as `--secret-file` during [installation](#Install-and-start-Velero).

1. Create a Workforce Identity Federation external credential file.

    ```bash
    gcloud iam workforce-pools create-cred-config \
        locations/global/workforcePools/WORKFORCE_POOL_ID/providers/PROVIDER_ID \
        --subject-token-type=urn:ietf:params:oauth:token-type:id_token \
        --credential-source-file=PATH_TO_OIDC_ID_TOKEN \
        --workforce-pool-user-project=WORKFORCE_POOL_USER_PROJECT \
        --output-file=config.json
    ```

#### Option 3: Using GKE Workload Identity

This requires a GKE cluster with workload identity enabled.

1. Create Velero Namespace
This is required because Kubernetes Service Account (step 2) resides in a namespace

    ```bash
    NAMESPACE=velero
    kubectl create namespace $NAMESPACE
    ```

1. Create Kubernetes Service Account
This is required when binding to the Google Service Account.
Namespace is already created in step 1 above.

    ```bash
    KSA_NAME=velero
    kubectl create serviceaccount $KSA_NAME --namespace $NAMESPACE
    ```

3. Add IAM Policy Binding for Velero's Kubernetes service account to a GCP service account

    ```bash
    gcloud iam service-accounts add-iam-policy-binding \
        --role roles/iam.workloadIdentityUser \
        --member "serviceAccount:$PROJECT_ID.svc.id.goog[$NAMESPACE/$KSA_NAME]" \
        $GSA_NAME@$PROJECT_ID.iam.gserviceaccount.com
    ```

4. Add annotation to Kubernetes Service Account

    ```bash
    kubectl annotate serviceaccount $KSA_NAME \
        --namespace $NAMESPACE \
        iam.gke.io/gcp-service-account=$GSA_NAME@$PROJECT_ID.iam.gserviceaccount.com
    ```

In this case:
- `[$NAMESPACE/$KSA_NAME]` are Kubernetes Namespace and Service Account created in step 1 and 2.
- `PROJECT_ID` is the [Google Project ID](#Create-Google-Service-Account) - Step 1 and
- `GSA_NAME` is the name of the [Google Service Account](#Create-Google-Service-Account) - Step 2.

For more information on configuring workload identity on GKE, look at the [official GCP documentation][24] for more details.

## Install and start Velero

[Download][4] Velero

Install Velero, including all prerequisites, into the cluster and start the deployment. This will create a namespace called `velero`, and place a deployment named `velero` in it.

**If using a Google Service Account Key**:

```bash
velero install \
    --provider gcp \
    --plugins velero/velero-plugin-for-gcp:v1.13.0 \
    --bucket $BUCKET \
    --secret-file ./credentials-velero
```

**If using Workload Identity**:

You must add a service account annotation to the Kubernetes service account so that it will know which GCP service account to use. You can do this during installation with `--sa-annotations`. Use the flag `--no-secret` so that Velero will know not to look for a key file. You must also add the GCP service account name in `--backup-location-config`.

```bash
velero install \
    --provider gcp \
    --plugins velero/velero-plugin-for-gcp:v1.13.0 \
    --bucket $BUCKET \
    --no-secret \
    --sa-annotations iam.gke.io/gcp-service-account=[$GSA_NAME]@[$PROJECT_ID].iam.gserviceaccount.com \
    --backup-location-config serviceAccount=[$GSA_NAME]@[$PROJECT_ID].iam.gserviceaccount.com \
```

Additionally, you can specify `--use-node-agent` to enable node agent support, and `--wait` to wait for the deployment to be ready.

(Optional) Specify [additional configurable parameters](backupstoragelocation.md) for the `--backup-location-config` flag.

(Optional) Specify [additional configurable parameters](volumesnapshotlocation.md) for the `--snapshot-location-config` flag.

(Optional) [Customize the Velero installation][9] further to meet your needs.

For more complex installation needs, use either the [Helm chart](https://github.com/vmware-tanzu/helm-charts), or add `--dry-run -o yaml` options for generating the YAML representation for the installation.

## Create an additional Backup Storage Location

If you are using Velero v1.6.0 or later, you can create additional GCP [Backup Storage Locations][13] that use their own credentials.
These can also be created alongside Backup Storage Locations that use other providers.

### Limitations
It is not possible to use different credentials for additional Backup Storage Locations if you are pod based authentication such as [Workload Identity][14].

### Prerequisites

* Velero 1.6.0 or later
* GCP plugin must be installed, either at install time, or by running `velero plugin add velero/velero-plugin-for-gcp:plugin-version`, replace the `plugin-version` with the corresponding value

### Configure GCS bucket and credentials

To configure a new Backup Storage Location with its own credentials, it is necessary to follow the steps above to [create the bucket to use][1] and to [generate the credentials file][15] to interact with that bucket.
Once you have created the credentials file, create a [Kubernetes Secret][16] in the Velero namespace that contains these credentials:

```bash
kubectl create secret generic -n velero bsl-credentials --from-file=gcp=</path/to/credentialsfile>
```

This will create a secret named `bsl-credentials` with a single key (`gcp`) which contains the contents of your credentials file.
The name and key of this secret will be given to Velero when creating the Backup Storage Location, so it knows which secret data to use.

### Create Backup Storage Location

Once the bucket and credentials have been configured, these can be used to create the new Backup Storage Location:

```bash
velero backup-location create <bsl-name> \
  --provider gcp \
  --bucket $BUCKET \
  --credential=bsl-credentials=gcp
```

The Backup Storage Location is ready to use when it has the phase `Available`.
You can check this with the following command:

```bash
velero backup-location get
```

To use this new Backup Storage Location when performing a backup, use the flag `--storage-location <bsl-name>` when running `velero backup create`.

## Configure the GCP plugin
The Velero GCP plugin contains two plugins: 
ObjectStore plugin: it's used to connect to the GCP Cloud Storage to manipulate the object files.
Please check the possible configuration options in the [BSL configuration document](backupstoragelocation.md).

VolumeSnapshotter plugin: it's used to manipulate the snapshots in GCP.
Please check the possible configuration options in the [VSL configuration document](volumesnapshotlocation.md).



[1]: #Create-an-GCS-bucket
[2]: #Set-permissions-for-Velero
[3]: #Install-and-start-Velero
[4]: https://velero.io/docs/install-overview/
[5]: https://cloud.google.com/sdk/docs/
[9]: https://velero.io/docs/customize-installation/
[10]: ./examples
[11]: https://velero.io/docs/faq/
[12]: #Create-an-additional-Backup-Storage-Location
[13]: https://velero.io/docs/latest/api-types/backupstoragelocation/
[14]: #option-2-set-permissions-with-using-workload-identity-optional
[15]: #option-1-set-permissions-with-a-service-account
[16]: https://kubernetes.io/docs/concepts/configuration/secret/
[21]: https://cloud.google.com/compute/docs/access/service-accounts
[22]: https://cloud.google.com/kubernetes-engine/docs/how-to/role-based-access-control#iam-rolebinding-bootstrap
[24]: https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity

[101]: https://github.com/vmware-tanzu/velero-plugin-for-gcp/workflows/Main%20CI/badge.svg
[102]: https://github.com/vmware-tanzu/velero-plugin-for-gcp/actions?query=workflow%3A"Main+CI"
[103]: https://github.com/vmware-tanzu/velero/issues/new/choose
