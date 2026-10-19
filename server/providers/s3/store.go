// Package s3 provides opt-in S3-compatible object storage and site publishing.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	hex "github.com/crazycatviking/hex/server"
)

type Config struct {
	Bucket          string
	Region          string
	Endpoint        string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	UsePathStyle    bool
	// Credentials overrides static credentials; otherwise the AWS default chain is used.
	Credentials aws.CredentialsProvider
	HTTPClient  aws.HTTPClient
	// Client overrides Region, Endpoint, credentials, HTTPClient and UsePathStyle.
	Client *awss3.Client
	// PresignExpires defaults to 15 minutes and must not exceed seven days.
	PresignExpires time.Duration
}

type Store struct {
	client    *awss3.Client
	presigner *awss3.PresignClient
	bucket    string
	prefix    string
	expires   time.Duration
}

var _ hex.ObjectStore = (*Store)(nil)

func New(ctx context.Context, cfg Config) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("S3 bucket is required: %w", fs.ErrInvalid)
	}
	prefix := strings.TrimSuffix(cfg.Prefix, "/")
	if cfg.Prefix != "" && !validKey(prefix) {
		return nil, fmt.Errorf("invalid S3 root prefix: %w", fs.ErrInvalid)
	}
	if cfg.PresignExpires == 0 {
		cfg.PresignExpires = 15 * time.Minute
	}
	if cfg.PresignExpires < time.Second || cfg.PresignExpires > 7*24*time.Hour {
		return nil, fmt.Errorf("invalid presign expiry: %w", fs.ErrInvalid)
	}
	client := cfg.Client
	if client == nil {
		if cfg.Endpoint != "" {
			u, err := url.Parse(cfg.Endpoint)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return nil, fmt.Errorf("invalid S3 endpoint: %w", fs.ErrInvalid)
			}
		}
		provider := cfg.Credentials
		if provider == nil && (cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" || cfg.SessionToken != "") {
			if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
				return nil, fmt.Errorf("both S3 access key and secret are required: %w", fs.ErrInvalid)
			}
			provider = credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken)
		}
		options := []func(*awsconfig.LoadOptions) error{}
		if cfg.Region != "" {
			options = append(options, awsconfig.WithRegion(cfg.Region))
		}
		if provider != nil {
			options = append(options, awsconfig.WithCredentialsProvider(provider))
		}
		if cfg.HTTPClient != nil {
			options = append(options, awsconfig.WithHTTPClient(cfg.HTTPClient))
		}
		loaded, err := awsconfig.LoadDefaultConfig(ctx, options...)
		if err != nil {
			return nil, fmt.Errorf("load S3 configuration: %w", err)
		}
		if loaded.Region == "" {
			loaded.Region = "us-east-1"
		}
		client = awss3.NewFromConfig(loaded, func(o *awss3.Options) {
			o.UsePathStyle = cfg.UsePathStyle
			if cfg.Endpoint != "" {
				o.BaseEndpoint = aws.String(cfg.Endpoint)
			}
			// Avoid optional streaming checksum extensions unsupported by some compatible servers.
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		})
	}
	if prefix != "" {
		prefix += "/"
	}
	return &Store{client: client, presigner: awss3.NewPresignClient(client), bucket: cfg.Bucket, prefix: prefix, expires: cfg.PresignExpires}, nil
}

func validKey(key string) bool {
	return key != "." && fs.ValidPath(key) && !strings.ContainsAny(key, "\\\x00")
}

func (s *Store) key(key string) (string, error) {
	if !validKey(key) {
		return "", fs.ErrInvalid
	}
	return s.prefix + key, nil
}

func storageError(operation string, err error) error {
	var api smithy.APIError
	if errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound") {
		return fmt.Errorf("%s: %w", operation, hex.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	full, err := s.key(key)
	if err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &s.bucket, Key: &full})
	if err != nil {
		return nil, storageError("open object", err)
	}
	// Ownership of the streaming body passes to the caller.
	return result.Body, nil
}

func (s *Store) Put(ctx context.Context, key string, source io.Reader) error {
	return s.put(ctx, key, source, -1)
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

// Spooling provides a seekable retry body and validates length before replacement.
func (s *Store) put(ctx context.Context, key string, source io.Reader, size int64) (err error) {
	full, err := s.key(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp("", "hex-s3-*")
	if err != nil {
		return fmt.Errorf("spool object: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close(), os.Remove(file.Name())) }()
	reader := io.Reader(contextReader{ctx, source})
	if size >= 0 {
		reader = io.LimitReader(reader, size)
	}
	written, err := io.Copy(file, reader)
	if err != nil {
		return fmt.Errorf("spool object: %w", err)
	}
	if size >= 0 {
		if written != size {
			return fmt.Errorf("object length %d, expected %d", written, size)
		}
		extra, err := io.Copy(io.Discard, io.LimitReader(contextReader{ctx, source}, 1))
		if err != nil {
			return err
		}
		if extra != 0 {
			return fmt.Errorf("object exceeds expected length %d", size)
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{Bucket: &s.bucket, Key: &full, Body: file, ContentLength: &written})
	if err != nil {
		return storageError("put object", err)
	}
	return nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]hex.Object, error) {
	if prefix != "" && !validKey(strings.TrimSuffix(prefix, "/")) {
		return nil, fs.ErrInvalid
	}
	full := s.prefix + prefix
	paginator := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &full}, func(o *awss3.ListObjectsV2PaginatorOptions) { o.StopOnDuplicateToken = true })
	objects := []hex.Object{}
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, storageError("list objects", err)
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			if !strings.HasPrefix(key, full) {
				continue
			}
			key = strings.TrimPrefix(key, s.prefix)
			if !validKey(key) {
				continue
			}
			objects = append(objects, hex.Object{Key: key, Size: aws.ToInt64(object.Size)})
		}
	}
	return objects, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	full, err := s.key(key)
	if err != nil {
		return err
	}
	// S3 DELETE is idempotent; HEAD preserves ObjectStore's missing-key error.
	if _, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.bucket, Key: &full}); err != nil {
		return storageError("inspect object before delete", err)
	}
	if _, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &s.bucket, Key: &full}); err != nil {
		return storageError("delete object", err)
	}
	return nil
}
