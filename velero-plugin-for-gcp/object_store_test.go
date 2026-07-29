/*
Copyright 2018 the Velero contributors.

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
	"errors"
	"io"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	pkgerrors "github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	velerotest "github.com/vmware-tanzu/velero/pkg/test"
)

type mockWriteCloser struct {
	closeErr error
	writeErr error
}

func (m *mockWriteCloser) Close() error {
	return m.closeErr
}

func (m *mockWriteCloser) Write(b []byte) (int, error) {
	return len(b), m.writeErr
}

func newMockWriteCloser(writeErr, closeErr error) *mockWriteCloser {
	return &mockWriteCloser{writeErr: writeErr, closeErr: closeErr}
}

type fakeWriter struct {
	wc *mockWriteCloser

	attrsErr error
}

func newFakeWriter(wc *mockWriteCloser) *fakeWriter {
	return &fakeWriter{wc: wc}
}

func (fw *fakeWriter) getWriteCloser(bucket, name string) io.WriteCloser {
	return fw.wc
}

func (fw *fakeWriter) getAttrs(bucket, key string) (*storage.ObjectAttrs, error) {
	return new(storage.ObjectAttrs), fw.attrsErr
}

func TestPutObject(t *testing.T) {
	tests := []struct {
		name        string
		writeErr    error
		closeErr    error
		expectedErr error
	}{
		{
			name:        "No errors returns nil",
			closeErr:    nil,
			writeErr:    nil,
			expectedErr: nil,
		},
		{
			name:        "Close() errors are returned",
			closeErr:    errors.New("error closing"),
			expectedErr: errors.New("error closing"),
		},
		{
			name:        "Write() errors are returned",
			writeErr:    errors.New("error writing"),
			expectedErr: errors.New("error writing"),
		},
		{
			name:        "Write errors supercede close errors",
			writeErr:    errors.New("error writing"),
			closeErr:    errors.New("error closing"),
			expectedErr: errors.New("error writing"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wc := newMockWriteCloser(test.writeErr, test.closeErr)
			o := newObjectStore(velerotest.NewLogger())
			o.bucketWriter = newFakeWriter(wc)

			err := o.PutObject("bucket", "key", strings.NewReader("contents"))
			assert.Equal(t, test.expectedErr, err)
		})
	}
}

func TestObjectExists(t *testing.T) {
	tests := []struct {
		name           string
		errorResponse  error
		expectedExists bool
		expectedError  string
	}{
		{
			name:           "exists",
			errorResponse:  nil,
			expectedExists: true,
		},
		{
			name:           "doesn't exist",
			errorResponse:  storage.ErrObjectNotExist,
			expectedExists: false,
		},
		{
			name:           "doesn't exist - wrapped error",
			errorResponse:  pkgerrors.Wrap(storage.ErrObjectNotExist, "googleapi: Error 404: No such object"),
			expectedExists: false,
		},
		{
			name:           "error checking for existence",
			errorResponse:  errors.New("bad"),
			expectedExists: false,
			expectedError:  "bad",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := newObjectStore(velerotest.NewLogger())
			w := newFakeWriter(nil)
			o.bucketWriter = w
			w.attrsErr = tc.errorResponse

			bucket := "b"
			key := "k"
			exists, err := o.ObjectExists(bucket, key)

			if tc.expectedError != "" {
				assert.EqualError(t, err, tc.expectedError)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, tc.expectedExists, exists)
		})
	}
}

func TestCreateSignedURL_emptyGoogleAccessID(t *testing.T) {
	o := newObjectStore(velerotest.NewLogger())
	// googleAccessID is empty — simulates external_account credentials with no serviceAccount in BSL config
	_, err := o.CreateSignedURL("bucket", "key", 0)
	require.EqualError(t, err, "GoogleAccessID is empty, perhaps using external_account credentials, cannot create signed URL")
}

func Test_getSecretAccountKey(t *testing.T) {
	type args struct {
		secretByte []byte
	}
	tests := []struct {
		name    string
		args    args
		want    credAccountKeys
		wantErr bool
	}{
		{
			name: "get secret service account account key",
			args: args{
				// test data from https://cloud.google.com/iam/docs/keys-create-delete
				secretByte: []byte(`{
  "type": "service_account",
  "project_id": "PROJECT_ID",
  "private_key_id": "KEY_ID",
  "private_key": "-----BEGIN PRIVATE KEY-----\nPRIVATE_KEY\n-----END PRIVATE KEY-----\n",
  "client_email": "SERVICE_ACCOUNT_EMAIL",
  "client_id": "CLIENT_ID",
  "auth_uri": "https://accounts.google.com/o/oauth2/auth",
  "token_uri": "https://accounts.google.com/o/oauth2/token",
  "auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
  "client_x509_cert_url": "https://www.googleapis.com/robot/v1/metadata/x509/SERVICE_ACCOUNT_EMAIL"
}
`),
			},
			want:    serviceAccountKey,
			wantErr: false,
		},
		{
			name: "get secret external account key",
			args: args{
				// test data from https://cloud.google.com/iam/docs/workforce-obtaining-short-lived-credentials
				secretByte: []byte(`{
  "type": "external_account",
  "audience": "//iam.googleapis.com/locations/global/workforcePools/WORKFORCE_POOL_ID/providers/PROVIDER_ID",
  "subject_token_type": "urn:ietf:params:oauth:token-type:id_token",
  "token_url": "https://sts.googleapis.com/v1/token",
  "workforce_pool_user_project": "WORKFORCE_POOL_USER_PROJECT",
  "credential_source": {
    "file": "PATH_TO_OIDC_CREDENTIALS_FILE"
  }
}
`),
			},
			want:    externalAccountKey,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getSecretAccountTypeKey(tt.args.secretByte)
			if (err != nil) != tt.wantErr {
				t.Errorf("getSecretAccountKey() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getSecretAccountKey() = %v, want %v", got, tt.want)
			}
		})
	}
}
