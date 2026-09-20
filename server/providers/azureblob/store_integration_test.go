package azureblob

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	hex "github.com/crazycatviking/hex/server"
)

func TestAzuriteStore(t *testing.T) {
	connection := os.Getenv("HEX_TEST_BLOB_CONNECTION_STRING")
	if connection == "" {
		t.Skip("set HEX_TEST_BLOB_CONNECTION_STRING to run against Azurite")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	container := "hextest-" + strings.ToLower(rand.Text())
	store, err := NewFromConnectionString(connection, container)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureContainer(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client, err := azblob.NewClientFromConnectionString(connection, nil)
		if err != nil {
			t.Error(err)
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := client.DeleteContainer(cleanup, container, nil); err != nil {
			t.Error(err)
		}
	})
	if err := store.EnsureContainer(ctx); err != nil {
		t.Fatal("container initialization must be idempotent", err)
	}

	payload := []byte{0, 255, 128, 10}
	if err := store.Put(ctx, "demo/nested/data.bin", bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFromConnectionString(connection, container)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := reopened.Open(ctx, "demo/nested/data.bin")
	if err != nil {
		t.Fatal(err)
	}
	data, readError := io.ReadAll(reader)
	closeError := reader.Close()
	if readError != nil || closeError != nil || !bytes.Equal(data, payload) {
		t.Fatalf("binary roundtrip failed: %v %v %v", data, readError, closeError)
	}
	if err := store.Put(ctx, "demo/nested/data.bin", strings.NewReader("replaced")); err != nil {
		t.Fatal(err)
	}
	objects, err := store.List(ctx, "demo/")
	if err != nil || len(objects) != 1 || objects[0].Size != 8 {
		t.Fatalf("unexpected listing: %v %v", objects, err)
	}
	objects, err = store.List(ctx, "other/")
	if err != nil || len(objects) != 0 {
		t.Fatalf("prefix leaked objects: %v %v", objects, err)
	}
	if err := store.Delete(ctx, "demo/nested/data.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, "demo/nested/data.bin"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := store.Delete(ctx, "demo/nested/data.bin"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected not found on deletion, got %v", err)
	}
}
