package azureblob

import (
	"context"
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
		return nil, err
	}
	return &Store{client: client, container: container}, nil
}

func (s *Store) Put(ctx context.Context, key string, reader io.Reader) error {
	_, err := s.client.UploadStream(ctx, s.container, key, reader, nil)
	return err
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	response, err := s.client.DownloadStream(ctx, s.container, key, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return nil, hex.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]hex.Object, error) {
	pager := s.client.NewListBlobsFlatPager(s.container, &azblob.ListBlobsFlatOptions{Prefix: &prefix})
	objects := []hex.Object{}
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, blob := range page.Segment.BlobItems {
			objects = append(objects, hex.Object{Key: *blob.Name, Size: *blob.Properties.ContentLength})
		}
	}
	return objects, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteBlob(ctx, s.container, key, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return hex.ErrNotFound
	}
	return err
}
