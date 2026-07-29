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
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	veleroplugin "github.com/vmware-tanzu/velero/pkg/plugin/framework"
)

const (
	kmsKeyNameConfigKey      = "kmsKeyName"
	serviceAccountConfigKey  = "serviceAccount"
	credentialsFileConfigKey = "credentialsFile"
	storeEndpointConfigKey   = "storeEndpoint"
	universeDomainKey        = "universeDomain"
)

// bucketWriter wraps the GCP SDK functions for accessing object store so they can be faked for testing.
type bucketWriter interface {
	// getWriteCloser returns an io.WriteCloser that can be used to upload data to the specified bucket for the specified key.
	getWriteCloser(bucket, key string) io.WriteCloser
	getAttrs(bucket, key string) (*storage.ObjectAttrs, error)
}

type writer struct {
	client     *storage.Client
	kmsKeyName string
}

func (w *writer) getWriteCloser(bucket, key string) io.WriteCloser {
	writer := w.client.Bucket(bucket).Object(key).NewWriter(context.Background())
	writer.KMSKeyName = w.kmsKeyName

	return writer
}

func (w *writer) getAttrs(bucket, key string) (*storage.ObjectAttrs, error) {
	return w.client.Bucket(bucket).Object(key).Attrs(context.Background())
}

type ObjectStore struct {
	log            logrus.FieldLogger
	client         *storage.Client
	googleAccessID string
	privateKey     []byte
	bucketWriter   bucketWriter
	iamSvc         *iamcredentials.Service
	fileCredType   credAccountKeys
}

func newObjectStore(logger logrus.FieldLogger) *ObjectStore {
	return &ObjectStore{log: logger}
}

type credAccountKeys string

// From https://github.com/golang/oauth2/blob/d3ed0bb246c8d3c75b63937d9a5eecff9c74d7fe/google/google.go#L95
const (
	serviceAccountKey  credAccountKeys = "service_account"
	externalAccountKey credAccountKeys = "external_account"
)

func getSecretAccountTypeKey(secretByte []byte) (credAccountKeys, error) {
	var f map[string]interface{}
	if err := json.Unmarshal(secretByte, &f); err != nil {
		return "", err
	}
	// following will panic if cannot cast to credAccountKeys
	return credAccountKeys(f["type"].(string)), nil
}

func (o *ObjectStore) Init(config map[string]string) error {
	if err := veleroplugin.ValidateObjectStoreConfigKeys(
		config,
		kmsKeyNameConfigKey,
		serviceAccountConfigKey,
		credentialsFileConfigKey,
		storeEndpointConfigKey,
		universeDomainKey,
	); err != nil {
		return err
	}
	// Find default token source to extract the GoogleAccessID
	ctx := context.Background()

	clientOptions := []option.ClientOption{
		option.WithScopes(storage.ScopeReadWrite),
	}

	// Credentials to use when creating signed URLs.
	var creds *google.Credentials
	var err error

	// Prioritize the credentials file path in config, if it exists
	if credentialsFile, ok := config[credentialsFileConfigKey]; ok {
		b, err := os.ReadFile(credentialsFile)
		if err != nil {
			return errors.Wrapf(err, "error reading provided credentials file %v", credentialsFile)
		}

		creds, err = google.CredentialsFromJSON(ctx, b)
		if err != nil {
			return errors.WithStack(err)
		}

		// If using a credentials file, we also need to pass it when creating the client.
		clientOptions = append(clientOptions, option.WithCredentialsFile(credentialsFile))
	} else {
		// If a credentials file does not exist in the config, fall back to
		// loading default credentials for signed URLs.
		creds, err = google.FindDefaultCredentials(ctx)
	}

	if err != nil {
		return errors.WithStack(err)
	}

	// if using a endpoint, we need to pass it when creating the object store client
	if endpoint, ok := config[storeEndpointConfigKey]; ok {
		clientOptions = append(clientOptions, option.WithEndpoint(endpoint))
	}

	// if using a universeDomain, we need to pass it when creating the object store client
	if universeDomain, ok := config[universeDomainKey]; ok {
		clientOptions = append(clientOptions, option.WithUniverseDomain(universeDomain))
	}

	if creds.JSON != nil {
		o.fileCredType, err = getSecretAccountTypeKey(creds.JSON)
		if err != nil {
			return errors.WithStack(err)
		}
		if o.fileCredType == serviceAccountKey {
			// Using Credentials File
			err = o.initFromKeyFile(creds)
		}
	} else {
		// Using compute engine credentials. Use this if workload identity is enabled.
		err = o.initFromComputeEngine(config)
	}

	if err != nil {
		return errors.WithStack(err)
	}

	client, err := storage.NewClient(ctx, clientOptions...)
	if err != nil {
		return errors.WithStack(err)
	}
	o.client = client

	o.bucketWriter = &writer{
		client:     o.client,
		kmsKeyName: config[kmsKeyNameConfigKey],
	}
	return nil
}

// This function is used to populate the googleAccessID and privateKey fields when using a service account credentials file.
// it will error if credential file is not for a service account.
// Do not run this function if using non SA credentials such as external_account.
func (o *ObjectStore) initFromKeyFile(creds *google.Credentials) error {
	jwtConfig, err := google.JWTConfigFromJSON(creds.JSON)
	if err != nil {
		return errors.Wrap(err, "error parsing credentials file; should be JSON")
	}
	if jwtConfig.Email == "" {
		return errors.Errorf("credentials file pointed to by %s does not contain an email", "GOOGLE_APPLICATION_CREDENTIALS")
	}
	if len(jwtConfig.PrivateKey) == 0 {
		return errors.Errorf("credentials file pointed to by %s does not contain a private key", "GOOGLE_APPLICATION_CREDENTIALS")
	}

	o.googleAccessID = jwtConfig.Email
	o.privateKey = jwtConfig.PrivateKey
	return nil
}

func (o *ObjectStore) initFromComputeEngine(config map[string]string) error {
	var err error
	var ok bool
	o.googleAccessID, ok = config["serviceAccount"]
	if !ok {
		return errors.Errorf("serviceAccount is expected to be provided as an item in BackupStorageLocation's config")
	}
	clientOptions := []option.ClientOption{}
	// if using a universeDomain, we need to pass it when initiate a new iam credentials service
	if universeDomain, ok := config[universeDomainKey]; ok {
		clientOptions = append(clientOptions, option.WithUniverseDomain(universeDomain))
	}
	o.iamSvc, err = iamcredentials.NewService(context.Background(), clientOptions...)
	return err
}

func (o *ObjectStore) PutObject(bucket, key string, body io.Reader) error {
	w := o.bucketWriter.getWriteCloser(bucket, key)

	// The writer returned by NewWriter is asynchronous, so errors aren't guaranteed
	// until Close() is called
	_, copyErr := io.Copy(w, body)

	// Ensure we close w and report errors properly
	closeErr := w.Close()
	if copyErr != nil {
		return copyErr
	}

	return closeErr
}

func (o *ObjectStore) ObjectExists(bucket, key string) (bool, error) {
	if _, err := o.bucketWriter.getAttrs(bucket, key); err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, errors.WithStack(err)
	}

	return true, nil
}

func (o *ObjectStore) GetObject(bucket, key string) (io.ReadCloser, error) {
	r, err := o.client.Bucket(bucket).Object(key).NewReader(context.Background())
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return r, nil
}

func (o *ObjectStore) ListCommonPrefixes(bucket, prefix, delimiter string) ([]string, error) {
	q := &storage.Query{
		Prefix:    prefix,
		Delimiter: delimiter,
	}

	iter := o.client.Bucket(bucket).Objects(context.Background(), q)

	var res []string
	for {
		obj, err := iter.Next()
		if err != nil && err != iterator.Done {
			return nil, errors.WithStack(err)
		}
		if err == iterator.Done {
			break
		}

		if obj.Prefix != "" {
			res = append(res, obj.Prefix)
		}
	}

	return res, nil
}

func (o *ObjectStore) ListObjects(bucket, prefix string) ([]string, error) {
	q := &storage.Query{
		Prefix: prefix,
	}

	var res []string

	iter := o.client.Bucket(bucket).Objects(context.Background(), q)

	for {
		obj, err := iter.Next()
		if err == iterator.Done {
			return res, nil
		}
		if err != nil {
			return nil, errors.WithStack(err)
		}

		res = append(res, obj.Name)
	}
}

func (o *ObjectStore) DeleteObject(bucket, key string) error {
	return errors.Wrapf(o.client.Bucket(bucket).Object(key).Delete(context.Background()), "error deleting object %s", key)
}

/*
 * Use the iamSignBlob api call to sign the url if there is no credentials file to get the key from.
 * https://cloud.google.com/iam/credentials/reference/rest/v1/projects.serviceAccounts/signBlob
 */
func (o *ObjectStore) SignBytes(bytes []byte) ([]byte, error) {
	name := "projects/-/serviceAccounts/" + o.googleAccessID
	resp, err := o.iamSvc.Projects.ServiceAccounts.SignBlob(name, &iamcredentials.SignBlobRequest{
		Payload: base64.StdEncoding.EncodeToString(bytes),
	}).Context(context.Background()).Do()

	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(resp.SignedBlob)
}

func (o *ObjectStore) CreateSignedURL(bucket, key string, ttl time.Duration) (string, error) {
	// googleAccessID is initialized from ServiceAccount key file and compute engine credentials.
	// If using external_account credentials, googleAccessID will be empty and we cannot create signed URL.
	if o.googleAccessID == "" {
		return "", errors.New("GoogleAccessID is empty, perhaps using external_account credentials, cannot create signed URL")
	}
	options := storage.SignedURLOptions{
		GoogleAccessID: o.googleAccessID,
		Method:         "GET",
		Expires:        time.Now().Add(ttl),
	}

	if o.privateKey == nil {
		options.SignBytes = o.SignBytes
	} else {
		options.PrivateKey = o.privateKey
	}

	return o.client.Bucket(bucket).SignedURL(key, &options)
}
