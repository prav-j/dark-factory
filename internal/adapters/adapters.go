// Package adapters wires execution-plane interfaces (harness.Checkpointer,
// stophook.Persister/BlobStore) to their durable backends: DynamoDB for loop
// state and manifests, S3 for recovery blobs.
package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/prav-j/dark-factory/internal/harness"
	"github.com/prav-j/dark-factory/internal/sessionstore"
	"github.com/prav-j/dark-factory/internal/stophook"
)

// HarnessCheckpointer implements harness.Checkpointer over DDB.
type HarnessCheckpointer struct {
	Store *sessionstore.Store
}

func (a *HarnessCheckpointer) Save(ctx context.Context, runID string, state *harness.RunState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return a.Store.SaveCheckpoint(ctx, runID, raw)
}

func (a *HarnessCheckpointer) Load(ctx context.Context, runID string) (*harness.RunState, error) {
	raw, err := a.Store.LoadCheckpoint(ctx, runID)
	if err != nil {
		return nil, err
	}
	var state harness.RunState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (a *HarnessCheckpointer) Delete(ctx context.Context, runID string) error {
	return a.Store.DeleteCheckpoint(ctx, runID)
}

var _ harness.Checkpointer = (*HarnessCheckpointer)(nil)

// ManifestPersister implements stophook.Persister over the Sessions table.
type ManifestPersister struct {
	Store *sessionstore.Store
}

func (p *ManifestPersister) SaveManifest(ctx context.Context, orgID, sessionID string, manifest []byte) error {
	return p.Store.SaveManifest(ctx, orgID, sessionID, manifest)
}

var _ stophook.Persister = (*ManifestPersister)(nil)

// S3BlobStore implements stophook.BlobStore over S3 (or LocalStack S3).
type S3BlobStore struct {
	Client *s3.Client
	Bucket string
}

// Upload stores the blob under key; the returned ref is "s3://bucket/key".
func (s *S3BlobStore) Upload(ctx context.Context, key string, data []byte) (string, error) {
	if s.Client == nil || s.Bucket == "" {
		return "", errors.New("blob store not configured")
	}
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.Bucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("s3://%s/%s", s.Bucket, key), nil
}

// Fetch retrieves a blob by ref ("s3://bucket/key").
func (s *S3BlobStore) Fetch(ctx context.Context, ref string) ([]byte, error) {
	var bucket, key string
	if n, _ := fmt.Sscanf(ref, "s3://%s", &key); n != 1 {
		return nil, errors.New("bad ref")
	}
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			bucket, key = key[:i], key[i+1:]
			break
		}
	}
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := out.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

var _ stophook.BlobStore = (*S3BlobStore)(nil)
