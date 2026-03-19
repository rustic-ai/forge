package filesystem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
	"google.golang.org/api/iterator"
)

type FileMeta struct {
	Filename      string         `json:"filename"`
	ContentType   string         `json:"content_type"`
	ContentLength int64          `json:"content_length"`
	UploadedAt    string         `json:"uploaded_at"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type LocalFileStore struct {
	resolver *FileSystemResolver
	mu       sync.Mutex
	buckets  map[string]*blob.Bucket
}

var (
	ErrFileAlreadyExists = errors.New("file already exists")
	ErrFileNotFound      = errors.New("file not found")
)

type FileRecord struct {
	Filename string
	MimeType string
	Metadata map[string]any
}

func NewLocalFileStore(resolver *FileSystemResolver) *LocalFileStore {
	if resolver == nil {
		resolver = NewFileSystemResolver("")
	}
	return &LocalFileStore{
		resolver: resolver,
		buckets:  map[string]*blob.Bucket{},
	}
}

func (s *LocalFileStore) Resolver() *FileSystemResolver {
	return s.resolver
}

func (s *LocalFileStore) Upload(
	ctx context.Context,
	cfg DependencyConfig,
	orgID, guildID, agentID, filename string,
	content []byte,
	contentType string,
	metadata map[string]any,
) error {
	scope, err := s.resolver.ResolveScope(cfg, orgID, guildID, agentID)
	if err != nil {
		return err
	}

	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}

	exists, err := s.Exists(ctx, cfg, orgID, guildID, agentID, filename)
	if err != nil {
		return err
	}
	if exists {
		return ErrFileAlreadyExists
	}

	cleanName, err := SanitizeFilename(filename)
	if err != nil {
		return err
	}
	key := objectKey(scope, cleanName)

	bucket, err := s.openBucket(ctx, scope)
	if err != nil {
		return err
	}

	writer, err := bucket.NewWriter(ctx, key, nil)
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %w", err)
	}
	if _, err := io.Copy(writer, bytes.NewReader(content)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("failed to write file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finalize file write: %w", err)
	}

	meta := map[string]any{}
	for k, v := range metadata {
		meta[k] = v
	}
	meta["content_length"] = len(content)
	meta["content_type"] = contentType
	meta["uploaded_at"] = time.Now().UTC().Format(time.RFC3339Nano)

	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	metaWriter, err := bucket.NewWriter(ctx, metaObjectKey(scope, cleanName), nil)
	if err != nil {
		return fmt.Errorf("failed to open metadata file for writing: %w", err)
	}
	if _, err := io.Copy(metaWriter, bytes.NewReader(metaBytes)); err != nil {
		_ = metaWriter.Close()
		return fmt.Errorf("failed to write metadata: %w", err)
	}
	if err := metaWriter.Close(); err != nil {
		return fmt.Errorf("failed to finalize metadata write: %w", err)
	}

	return nil
}

func (s *LocalFileStore) Exists(
	ctx context.Context,
	cfg DependencyConfig,
	orgID, guildID, agentID, filename string,
) (bool, error) {
	scope, err := s.resolver.ResolveScope(cfg, orgID, guildID, agentID)
	if err != nil {
		return false, err
	}
	cleanName, err := SanitizeFilename(filename)
	if err != nil {
		return false, err
	}
	bucket, err := s.openBucket(ctx, scope)
	if err != nil {
		return false, err
	}
	return bucket.Exists(ctx, objectKey(scope, cleanName))
}

func (s *LocalFileStore) Read(
	ctx context.Context,
	cfg DependencyConfig,
	orgID, guildID, agentID, filename string,
) ([]byte, error) {
	scope, err := s.resolver.ResolveScope(cfg, orgID, guildID, agentID)
	if err != nil {
		return nil, err
	}
	cleanName, err := SanitizeFilename(filename)
	if err != nil {
		return nil, err
	}
	bucket, err := s.openBucket(ctx, scope)
	if err != nil {
		return nil, err
	}

	key := objectKey(scope, cleanName)
	exists, err := bucket.Exists(ctx, key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrFileNotFound
	}

	reader, err := bucket.NewReader(ctx, key, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()

	return io.ReadAll(reader)
}

func (s *LocalFileStore) List(
	ctx context.Context,
	cfg DependencyConfig,
	orgID, guildID, agentID string,
) ([]FileRecord, error) {
	scope, err := s.resolver.ResolveScope(cfg, orgID, guildID, agentID)
	if err != nil {
		return nil, err
	}
	bucket, err := s.openBucket(ctx, scope)
	if err != nil {
		return nil, err
	}

	prefix := prefixWithSlash(scope.ObjectPath)
	iter := bucket.List(&blob.ListOptions{Prefix: prefix})
	results := make([]FileRecord, 0)

	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, iterator.Done) || errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if obj == nil {
			break
		}
		name := strings.TrimPrefix(obj.Key, prefix)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.Contains(name, "/") {
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}

		meta, err := s.readMeta(ctx, bucket, scope, name)
		if err != nil {
			meta = map[string]any{}
		}

		contentType, _ := meta["content_type"].(string)
		if strings.TrimSpace(contentType) == "" {
			contentType = guessMimeType(name)
		}

		userMeta := map[string]any{}
		for k, v := range meta {
			if k == "content_type" {
				continue
			}
			userMeta[k] = v
		}
		var outMeta map[string]any
		if len(userMeta) > 0 {
			outMeta = userMeta
		}

		results = append(results, FileRecord{
			Filename: name,
			MimeType: contentType,
			Metadata: outMeta,
		})
	}

	return results, nil
}

func (s *LocalFileStore) Delete(
	ctx context.Context,
	cfg DependencyConfig,
	orgID, guildID, agentID, filename string,
) error {
	scope, err := s.resolver.ResolveScope(cfg, orgID, guildID, agentID)
	if err != nil {
		return err
	}
	cleanName, err := SanitizeFilename(filename)
	if err != nil {
		return err
	}
	bucket, err := s.openBucket(ctx, scope)
	if err != nil {
		return err
	}

	key := objectKey(scope, cleanName)
	exists, err := bucket.Exists(ctx, key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrFileNotFound
	}
	if err := bucket.Delete(ctx, key); err != nil {
		return err
	}

	metaKey := metaObjectKey(scope, cleanName)
	metaExists, err := bucket.Exists(ctx, metaKey)
	if err != nil {
		return err
	}
	if !metaExists {
		return fmt.Errorf("metadata not found for %s", cleanName)
	}
	if err := bucket.Delete(ctx, metaKey); err != nil {
		return err
	}

	return nil
}

func (s *LocalFileStore) readMeta(ctx context.Context, bucket *blob.Bucket, scope Scope, filename string) (map[string]any, error) {
	reader, err := bucket.NewReader(ctx, metaObjectKey(scope, filename), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()

	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	return meta, nil
}

func (s *LocalFileStore) openBucket(ctx context.Context, scope Scope) (*blob.Bucket, error) {
	s.mu.Lock()
	if b, ok := s.buckets[scope.BucketURL]; ok {
		s.mu.Unlock()
		return b, nil
	}
	s.mu.Unlock()

	if scope.Protocol == "file" && scope.LocalRoot != "" {
		if err := ensureDir(scope.LocalRoot); err != nil {
			return nil, err
		}
	}

	bucket, err := blob.OpenBucket(ctx, scope.BucketURL)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.buckets[scope.BucketURL] = bucket
	s.mu.Unlock()
	return bucket, nil
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return nil
}

func objectKey(scope Scope, filename string) string {
	return path.Join(scope.ObjectPath, filename)
}

func metaObjectKey(scope Scope, filename string) string {
	return objectKey(scope, "."+filename+".meta")
}

func prefixWithSlash(prefix string) string {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return ""
	}
	return strings.TrimSuffix(p, "/") + "/"
}

func guessMimeType(filename string) string {
	if ext := path.Ext(filename); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	return "application/octet-stream"
}
