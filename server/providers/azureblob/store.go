package azureblob

import (
	"context"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	hex "github.com/hex-platform/hex/server"
)

type Store struct {
	client    *azblob.Client
	container string
}

func New(endpoint, container string, credential azcore.TokenCredential) (*Store, error) {
	client, err := azblob.NewClient(endpoint, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure Blob client: %w", err)
	}

	return &Store{client: client, container: container}, nil
}

func NewFromConnectionString(connectionString, container string) (*Store, error) {
	client, err := azblob.NewClientFromConnectionString(connectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure Blob client from connection string: %w", err)
	}

	return &Store{client: client, container: container}, nil
}

func (s *Store) EnsureContainer(ctx context.Context) error {
	_, err := s.client.CreateContainer(ctx, s.container, nil)
	if bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create blob container %q: %w", s.container, err)
	}

	return nil
}

func (s *Store) Put(ctx context.Context, key string, reader io.Reader) error {
	_, err := s.client.UploadStream(ctx, s.container, key, reader, nil)
	if err != nil {
		return fmt.Errorf("upload blob %q: %w", key, err)
	}

	return nil
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	response, err := s.client.DownloadStream(ctx, s.container, key, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return nil, hex.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("download blob %q: %w", key, err)
	}

	return response.Body, nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]hex.Object, error) {
	options := &azblob.ListBlobsFlatOptions{Prefix: &prefix}
	pager := s.client.NewListBlobsFlatPager(s.container, options)
	objects := []hex.Object{}

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list blobs under %q: %w", prefix, err)
		}

		for _, blob := range page.Segment.BlobItems {
			objects = append(objects, hex.Object{
				Key:  *blob.Name,
				Size: *blob.Properties.ContentLength,
			})
		}
	}

	return objects, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteBlob(ctx, s.container, key, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return hex.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete blob %q: %w", key, err)
	}

	return nil
}
