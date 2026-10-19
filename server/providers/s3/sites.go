package s3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	hex "github.com/crazycatviking/hex/server"
)

var (
	_ hex.SitePublisher      = (*Store)(nil)
	_ hex.SiteDirectory      = (*Store)(nil)
	_ hex.SiteMetadataReader = (*Store)(nil)
	_ hex.DirectUploader     = (*Store)(nil)
)

func sitePrefix(site string) (string, error) {
	if !validKey(site) || strings.Contains(site, "/") {
		return "", fs.ErrInvalid
	}
	return "public/sites/" + site + "/", nil
}

func siteKey(site, name string) (string, error) {
	prefix, err := sitePrefix(site)
	if err != nil {
		return "", err
	}
	if !validKey(name) {
		return "", fs.ErrInvalid
	}
	return prefix + name, nil
}

func (s *Store) ListSiteFiles(ctx context.Context, site string) ([]hex.SiteFile, error) {
	prefix, err := sitePrefix(site)
	if err != nil {
		return nil, err
	}
	objects, err := s.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	files := make([]hex.SiteFile, 0, len(objects))
	for _, object := range objects {
		files = append(files, hex.SiteFile{Path: strings.TrimPrefix(object.Key, prefix), Size: object.Size})
	}
	return files, nil
}

func (s *Store) ReadSiteFile(ctx context.Context, site, name string) (io.ReadCloser, error) {
	key, err := siteKey(site, name)
	if err != nil {
		return nil, err
	}
	return s.Open(ctx, key)
}

func (s *Store) WriteSiteFile(ctx context.Context, site, name string, size int64, source io.Reader) error {
	if size < 0 {
		return fs.ErrInvalid
	}
	key, err := siteKey(site, name)
	if err != nil {
		return err
	}
	return s.put(ctx, key, source, size)
}

func (s *Store) DeleteSiteFile(ctx context.Context, site, name string) error {
	key, err := siteKey(site, name)
	if err != nil {
		return err
	}
	return s.Delete(ctx, key)
}

func (s *Store) DeleteSite(ctx context.Context, site string) error {
	files, err := s.ListSiteFiles(ctx, site)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := s.DeleteSiteFile(ctx, site, file.Path); err != nil && !errors.Is(err, hex.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (s *Store) ListSites(ctx context.Context) ([]string, error) {
	objects, err := s.List(ctx, "public/sites/")
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, object := range objects {
		name, file, ok := strings.Cut(strings.TrimPrefix(object.Key, "public/sites/"), "/")
		if ok && file == "index.html" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) ReadSiteMetadata(ctx context.Context, site string) (*hex.SiteMetadata, error) {
	body, err := s.ReadSiteFile(ctx, site, ".hex-site.json")
	if errors.Is(err, hex.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(body, 64*1024+1))
	if err := errors.Join(readErr, body.Close()); err != nil {
		return nil, fmt.Errorf("read site metadata: %w", err)
	}
	if len(data) > 64*1024 {
		return nil, fmt.Errorf("site metadata exceeds 64 KiB")
	}
	var metadata hex.SiteMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parse site metadata: %w", err)
	}
	return &metadata, nil
}

func (s *Store) UploadTargets(ctx context.Context, site string, files []hex.SiteFile) ([]hex.UploadTarget, error) {
	if _, err := sitePrefix(site); err != nil {
		return nil, err
	}
	targets := make([]hex.UploadTarget, 0, len(files))
	for _, file := range files {
		key, err := siteKey(site, file.Path)
		if err != nil {
			return nil, err
		}
		if file.Size < 0 {
			return nil, fs.ErrInvalid
		}
		input := &awss3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(s.prefix + key), ContentLength: &file.Size}
		if file.MD5 != "" {
			digest, err := base64.StdEncoding.DecodeString(file.MD5)
			if err != nil || len(digest) != 16 {
				return nil, fmt.Errorf("invalid Content-MD5: %w", fs.ErrInvalid)
			}
			input.ContentMD5 = &file.MD5
		}
		result, err := s.presigner.PresignPutObject(ctx, input, func(o *awss3.PresignOptions) { o.Expires = s.expires })
		if err != nil {
			return nil, storageError("presign site file", err)
		}
		headers := map[string]string{"Content-Length": strconv.FormatInt(file.Size, 10)}
		for name, values := range result.SignedHeader {
			if strings.EqualFold(name, "Host") {
				continue
			}
			headers[name] = strings.Join(values, ",")
		}
		targets = append(targets, hex.UploadTarget{Path: file.Path, Protocol: "http", URL: result.URL, Headers: headers})
	}
	return targets, nil
}
