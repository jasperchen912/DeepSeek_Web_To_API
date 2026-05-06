package responsecache

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

const (
	defaultMemoryTTL      = 5 * time.Minute
	defaultDiskTTL        = 4 * time.Hour
	defaultMaxBody        = 64 << 20
	defaultMemoryMaxBytes = int64(3800 * 1000 * 1000)
	defaultDiskMaxBytes   = int64(16 * 1000 * 1000 * 1000)
	recordVersion         = 1
)

type CallerResolver interface {
	DetermineCaller(req *http.Request) (*auth.RequestAuth, error)
}

type HitFunc func(req *http.Request, entry Entry, source string)

type Options struct {
	Dir            string
	MemoryTTL      time.Duration
	DiskTTL        time.Duration
	MaxBody        int64
	MemoryMaxBytes int64
	DiskMaxBytes   int64
	OnHit          HitFunc
}

type Cache struct {
	mu             sync.Mutex
	dir            string
	memoryTTL      time.Duration
	diskTTL        time.Duration
	maxBody        int64
	memoryMaxBytes int64
	diskMaxBytes   int64
	memoryBytes    int64
	hits           int64
	misses         int64
	stores         int64
	memoryHits     int64
	diskHits       int64
	uncacheable    map[string]int64
	items          map[string]memoryEntry
	lastDiskSweep  time.Time
	onHit          HitFunc
}

type Entry struct {
	Status        int
	Header        http.Header
	Body          []byte
	DiskExpiresAt time.Time
}

type memoryEntry struct {
	entry           Entry
	memoryExpiresAt time.Time
	size            int64
}

type diskRecord struct {
	Version       int         `json:"version"`
	Key           string      `json:"key"`
	CreatedAtUnix int64       `json:"created_at_unix"`
	ExpiresAtUnix int64       `json:"expires_at_unix"`
	Status        int         `json:"status"`
	Header        http.Header `json:"header"`
	Body          []byte      `json:"body"`
}

func New(opts Options) *Cache {
	memoryTTL := opts.MemoryTTL
	if memoryTTL <= 0 {
		memoryTTL = defaultMemoryTTL
	}
	diskTTL := opts.DiskTTL
	if diskTTL <= 0 {
		diskTTL = defaultDiskTTL
	}
	maxBody := opts.MaxBody
	if maxBody <= 0 {
		maxBody = defaultMaxBody
	}
	memoryMaxBytes := opts.MemoryMaxBytes
	if memoryMaxBytes <= 0 {
		memoryMaxBytes = defaultMemoryMaxBytes
	}
	diskMaxBytes := opts.DiskMaxBytes
	if diskMaxBytes <= 0 {
		diskMaxBytes = defaultDiskMaxBytes
	}
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		dir = config.ResponseCacheDir()
	}
	return &Cache{
		dir:            filepath.Clean(dir),
		memoryTTL:      memoryTTL,
		diskTTL:        diskTTL,
		maxBody:        maxBody,
		memoryMaxBytes: memoryMaxBytes,
		diskMaxBytes:   diskMaxBytes,
		uncacheable:    map[string]int64{},
		items:          map[string]memoryEntry{},
		onHit:          opts.OnHit,
	}
}

func (c *Cache) Middleware(resolver CallerResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return c.Wrap(resolver, next)
	}
}

func (c *Cache) Wrap(resolver CallerResolver, next http.Handler) http.Handler {
	if c == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CacheableRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if c.requestBodyTooLarge(r) {
			next.ServeHTTP(w, r)
			return
		}
		rawBody, tooLarge, err := c.readRequestBody(r)
		if err != nil {
			r.Body = replayBody(rawBody, r.Body)
			next.ServeHTTP(w, r)
			return
		}
		if tooLarge {
			r.Body = replayBody(rawBody, r.Body)
			next.ServeHTTP(w, r)
			return
		}
		if closeErr := r.Body.Close(); closeErr != nil {
			config.Logger.Warn("[response_cache] close request body failed", "error", closeErr)
		}
		r.Body = io.NopCloser(bytes.NewReader(rawBody))

		owner := ""
		if resolver != nil {
			if a, authErr := resolver.DetermineCaller(r); authErr == nil && a != nil {
				owner = strings.TrimSpace(a.CallerID)
			}
		}
		if owner == "" {
			next.ServeHTTP(w, r)
			return
		}

		path := canonicalRequestPath(r.URL.Path)
		if requestBodyStreamEnabled(rawBody) {
			c.recordUncacheable("stream_request")
			config.Logger.Info("[response_cache] uncacheable", "path", path, "owner", owner, "reason", "stream_request", "request_body_bytes", len(rawBody))
			r.Body = io.NopCloser(bytes.NewReader(rawBody))
			next.ServeHTTP(w, r)
			return
		}

		key := RequestKey(r, owner, rawBody)
		if entry, source, ok := c.Get(key); ok {
			config.Logger.Info("[response_cache] hit", "path", path, "source", source, "key", key, "owner", owner, "status", entry.Status, "request_body_bytes", len(rawBody), "response_body_bytes", len(entry.Body))
			if c.onHit != nil {
				c.onHit(r, cloneEntry(entry), source)
			}
			writeCachedResponse(w, entry, source)
			return
		}
		config.Logger.Info("[response_cache] miss", "path", path, "key", key, "owner", owner, "request_body_bytes", len(rawBody))

		r.Body = io.NopCloser(bytes.NewReader(rawBody))
		cw := newCaptureResponseWriter(w, c.maxBody)
		next.ServeHTTP(cw, r)
		if entry, ok, reason := cw.cacheEntry(); ok {
			c.Set(key, entry)
			config.Logger.Info("[response_cache] store", "path", path, "key", key, "owner", owner, "status", entry.Status, "response_body_bytes", len(entry.Body))
		} else {
			c.recordUncacheable(reason)
			config.Logger.Info("[response_cache] uncacheable", "path", path, "key", key, "owner", owner, "reason", reason)
		}
	})
}

func requestBodyStreamEnabled(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var value struct {
		Stream any `json:"stream"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return false
	}
	switch v := value.Stream.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func CacheableRequest(r *http.Request) bool {
	if r == nil || r.URL == nil || r.Method != http.MethodPost {
		return false
	}
	if requestForcesBypass(r) {
		return false
	}
	path := canonicalRequestPath(r.URL.Path)
	switch path {
	case "/v1/chat/completions", "/v1/responses", "/v1/embeddings":
		return true
	case "/anthropic/v1/messages", "/v1/messages", "/messages",
		"/anthropic/v1/messages/count_tokens", "/v1/messages/count_tokens", "/messages/count_tokens":
		return true
	}
	if strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/v1/models/") {
		return strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent")
	}
	return false
}

func RequestKey(r *http.Request, owner string, body []byte) string {
	h := sha256.New()
	body = canonicalRequestBodyForKey(r, body)
	writeKeyPart(h, "v1")
	writeKeyPart(h, strings.TrimSpace(owner))
	if r != nil {
		writeKeyPart(h, strings.ToUpper(strings.TrimSpace(r.Method)))
		if r.URL != nil {
			writeKeyPart(h, canonicalRequestPath(r.URL.Path))
			writeKeyPart(h, canonicalQuery(r.URL.Query()))
		}
		for _, header := range varyRequestHeaders() {
			writeKeyPart(h, strings.ToLower(header.Name)+":"+strings.Join(requestHeaderValues(r.Header, header.Name, header.Legacy...), "\x1f"))
		}
	}
	bodySum := sha256.Sum256(body)
	writeKeyPart(h, hex.EncodeToString(bodySum[:]))
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalRequestBodyForKey(r *http.Request, body []byte) []byte {
	if len(body) == 0 || !cacheKeyShouldNormalizeJSON(r) {
		return body
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return body
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return body
	}
	value, keep := normalizeCacheKeyJSONValue(value, true)
	if !keep {
		return body
	}
	normalized, err := json.Marshal(value)
	if err != nil || len(normalized) == 0 {
		return body
	}
	return normalized
}

func cacheKeyShouldNormalizeJSON(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if isJSONContentType(r.Header.Get("Content-Type")) {
		return true
	}
	return CacheableRequest(r)
}

func isJSONContentType(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return raw == "application/json" || strings.Contains(raw, "+json") || strings.Contains(raw, "/json")
}

func normalizeCacheKeyJSONValue(value any, topLevel bool) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		if cacheKeyMapShouldDrop(v) {
			return nil, false
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			if ignoredCacheKeyField(key, topLevel) {
				continue
			}
			normalized, keep := normalizeCacheKeyJSONValue(item, false)
			if !keep {
				continue
			}
			out[key] = normalized
		}
		return out, true
	case []any:
		out := make([]any, 0, len(v))
		for i := range v {
			normalized, keep := normalizeCacheKeyJSONValue(v[i], false)
			if !keep {
				continue
			}
			out = append(out, normalized)
		}
		return out, true
	default:
		return value, true
	}
}

func cacheKeyMapShouldDrop(value map[string]any) bool {
	typ, _ := value["type"].(string)
	return strings.EqualFold(strings.TrimSpace(typ), "cache_edits")
}

func ignoredCacheKeyField(key string, topLevel bool) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "cache_control", "cache_reference", "context_management":
		return true
	}
	if !topLevel {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "metadata", "user", "service_tier", "parallel_tool_calls", "seed", "store", "betas":
		return true
	default:
		return false
	}
}

func (c *Cache) Get(key string) (Entry, string, bool) {
	key = normalizeKey(key)
	if key == "" {
		return Entry{}, "", false
	}
	now := time.Now()
	c.mu.Lock()
	c.sweepMemoryLocked(now)
	if item, ok := c.items[key]; ok {
		if now.Before(item.memoryExpiresAt) && now.Before(item.entry.DiskExpiresAt) {
			entry := cloneEntry(item.entry)
			c.recordHitLocked("memory")
			c.mu.Unlock()
			return entry, "memory", true
		}
		delete(c.items, key)
	}
	c.mu.Unlock()

	entry, ok := c.getDisk(key, now)
	if !ok {
		c.recordMiss()
		return Entry{}, "", false
	}
	c.putMemory(key, entry, now)
	c.recordHit("disk")
	return cloneEntry(entry), "disk", true
}

func (c *Cache) Set(key string, entry Entry) {
	key = normalizeKey(key)
	if key == "" || !entry.cacheable(c.maxBody) {
		return
	}
	now := time.Now()
	if entry.DiskExpiresAt.IsZero() || !entry.DiskExpiresAt.After(now) {
		entry.DiskExpiresAt = now.Add(c.diskTTL)
	}
	entry = cloneEntry(entry)
	c.putMemory(key, entry, now)
	if err := c.putDisk(key, entry, now); err != nil {
		config.Logger.Warn("[response_cache] disk write failed", "error", err)
	} else {
		c.enforceDiskLimit(now)
	}
	c.recordStore()
	c.maybeSweepDisk(now)
}

func (c *Cache) requestBodyTooLarge(r *http.Request) bool {
	if c.maxBody <= 0 || r == nil {
		return false
	}
	return r.ContentLength > c.maxBody
}

func (c *Cache) readRequestBody(r *http.Request) ([]byte, bool, error) {
	if r == nil || r.Body == nil {
		return nil, false, nil
	}
	if c.maxBody <= 0 {
		raw, err := io.ReadAll(r.Body)
		return raw, false, err
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, c.maxBody+1))
	if err != nil {
		return raw, false, err
	}
	if int64(len(raw)) > c.maxBody {
		return raw, true, nil
	}
	return raw, false, nil
}

type replayReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r replayReadCloser) Close() error {
	if r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

func replayBody(prefix []byte, rest io.ReadCloser) io.ReadCloser {
	if rest == nil {
		return io.NopCloser(bytes.NewReader(prefix))
	}
	return replayReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), rest),
		closer: rest,
	}
}

func canonicalRequestPath(path string) string {
	path = strings.TrimSpace(path)
	switch path {
	case "/chat/completions", "/v1/v1/chat/completions":
		return "/v1/chat/completions"
	case "/responses", "/v1/v1/responses":
		return "/v1/responses"
	case "/embeddings", "/v1/v1/embeddings":
		return "/v1/embeddings"
	case "/anthropic/v1/messages", "/messages", "/v1/v1/messages":
		return "/v1/messages"
	case "/anthropic/v1/messages/count_tokens", "/messages/count_tokens", "/v1/v1/messages/count_tokens":
		return "/v1/messages/count_tokens"
	default:
		return path
	}
}

func (c *Cache) putMemory(key string, entry Entry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepMemoryLocked(now)
	size := entry.memorySize()
	if c.memoryMaxBytes > 0 && size > c.memoryMaxBytes {
		return
	}
	expiresAt := now.Add(c.memoryTTL)
	if entry.DiskExpiresAt.Before(expiresAt) {
		expiresAt = entry.DiskExpiresAt
	}
	if expiresAt.After(now) {
		if old, ok := c.items[key]; ok {
			c.memoryBytes -= old.size
		}
		c.items[key] = memoryEntry{entry: cloneEntry(entry), memoryExpiresAt: expiresAt, size: size}
		c.memoryBytes += size
		c.enforceMemoryLimitLocked()
	}
}

func (c *Cache) sweepMemoryLocked(now time.Time) {
	for key, item := range c.items {
		if !now.Before(item.memoryExpiresAt) || !now.Before(item.entry.DiskExpiresAt) {
			delete(c.items, key)
			c.memoryBytes -= item.size
		}
	}
	if c.memoryBytes < 0 {
		c.memoryBytes = 0
	}
}

func (c *Cache) enforceMemoryLimitLocked() {
	if c.memoryMaxBytes <= 0 {
		return
	}
	for c.memoryBytes > c.memoryMaxBytes && len(c.items) > 0 {
		var oldestKey string
		var oldest memoryEntry
		first := true
		for key, item := range c.items {
			if first || item.memoryExpiresAt.Before(oldest.memoryExpiresAt) {
				oldestKey = key
				oldest = item
				first = false
			}
		}
		delete(c.items, oldestKey)
		c.memoryBytes -= oldest.size
	}
	if c.memoryBytes < 0 {
		c.memoryBytes = 0
	}
}

func (c *Cache) recordHit(source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordHitLocked(source)
}

func (c *Cache) recordHitLocked(source string) {
	c.hits++
	switch source {
	case "memory":
		c.memoryHits++
	case "disk":
		c.diskHits++
	}
}

func (c *Cache) recordMiss() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.misses++
}

func (c *Cache) recordStore() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stores++
}

func (c *Cache) recordUncacheable(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unknown"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.uncacheable == nil {
		c.uncacheable = map[string]int64{}
	}
	c.uncacheable[reason]++
}

func (c *Cache) putDisk(key string, entry Entry, now time.Time) error {
	if c.dir == "" {
		return nil
	}
	path, ok := c.diskPath(key)
	if !ok {
		return fmt.Errorf("invalid response cache path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	gz := gzip.NewWriter(tmp)
	record := diskRecord{
		Version:       recordVersion,
		Key:           key,
		CreatedAtUnix: now.Unix(),
		ExpiresAtUnix: entry.DiskExpiresAt.Unix(),
		Status:        entry.Status,
		Header:        cloneHeader(entry.Header),
		Body:          append([]byte(nil), entry.Body...),
	}
	encodeErr := json.NewEncoder(gz).Encode(record)
	closeGZErr := gz.Close()
	closeFileErr := tmp.Close()
	if encodeErr != nil {
		return encodeErr
	}
	if closeGZErr != nil {
		return closeGZErr
	}
	if closeFileErr != nil {
		return closeFileErr
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

func (c *Cache) getDisk(key string, now time.Time) (Entry, bool) {
	if c.dir == "" {
		return Entry{}, false
	}
	path, ok := c.diskPath(key)
	if !ok {
		return Entry{}, false
	}
	// #nosec G304 -- path is derived from a normalized SHA-256 cache key and root-checked.
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, false
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			config.Logger.Warn("[response_cache] close cache file failed", "error", closeErr)
		}
	}()
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = removeDiskCacheFile(c.dir, path)
		return Entry{}, false
	}
	defer func() {
		if closeErr := gz.Close(); closeErr != nil {
			config.Logger.Warn("[response_cache] close gzip reader failed", "error", closeErr)
		}
	}()
	var record diskRecord
	if err := json.NewDecoder(gz).Decode(&record); err != nil {
		_ = removeDiskCacheFile(c.dir, path)
		return Entry{}, false
	}
	expiresAt := time.Unix(record.ExpiresAtUnix, 0)
	if record.Version != recordVersion || record.Key != key || !expiresAt.After(now) {
		_ = removeDiskCacheFile(c.dir, path)
		return Entry{}, false
	}
	entry := Entry{
		Status:        record.Status,
		Header:        sanitizeResponseHeader(record.Header),
		Body:          append([]byte(nil), record.Body...),
		DiskExpiresAt: expiresAt,
	}
	if !entry.cacheable(c.maxBody) {
		_ = removeDiskCacheFile(c.dir, path)
		return Entry{}, false
	}
	return entry, true
}

func (c *Cache) maybeSweepDisk(now time.Time) {
	c.mu.Lock()
	if now.Sub(c.lastDiskSweep) < 10*time.Minute {
		c.mu.Unlock()
		return
	}
	c.lastDiskSweep = now
	dir := c.dir
	diskTTL := c.diskTTL
	c.mu.Unlock()

	if dir == "" {
		return
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || !strings.HasSuffix(path, ".json.gz") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if now.Sub(info.ModTime()) > diskTTL {
			_ = removeDiskCacheFile(dir, path)
		}
		return nil
	})
}

type diskCacheFile struct {
	path    string
	size    int64
	modTime time.Time
}

func (c *Cache) enforceDiskLimit(now time.Time) {
	if c.dir == "" || c.diskMaxBytes <= 0 {
		return
	}
	files, total := c.diskFiles(now)
	if total <= c.diskMaxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})
	for _, file := range files {
		if total <= c.diskMaxBytes {
			return
		}
		if err := removeDiskCacheFile(c.dir, file.path); err == nil {
			total -= file.size
		}
	}
}

func (c *Cache) diskFiles(now time.Time) ([]diskCacheFile, int64) {
	files := []diskCacheFile{}
	var total int64
	_ = filepath.WalkDir(c.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || !strings.HasSuffix(path, ".json.gz") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if c.diskTTL > 0 && now.Sub(info.ModTime()) > c.diskTTL {
			_ = removeDiskCacheFile(c.dir, path)
			return nil
		}
		size := info.Size()
		total += size
		files = append(files, diskCacheFile{path: path, size: size, modTime: info.ModTime()})
		return nil
	})
	return files, total
}

func (c *Cache) diskPath(key string) (string, bool) {
	key = normalizeKey(key)
	if key == "" {
		return "", false
	}
	var path string
	if len(key) < 2 {
		path = filepath.Join(c.dir, key+".json.gz")
	} else {
		path = filepath.Join(c.dir, key[:2], key+".json.gz")
	}
	return path, pathWithinDir(c.dir, path)
}

func removeDiskCacheFile(root, path string) error {
	if !pathWithinDir(root, path) {
		return fmt.Errorf("response cache path escapes root")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	// #nosec G122 -- path is root-checked and symlinks are ignored before removal.
	return os.Remove(path)
}

func pathWithinDir(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeCachedResponse(w http.ResponseWriter, entry Entry, source string) {
	for k, vv := range sanitizeResponseHeader(entry.Header) {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-DeepSeek-Web-To-API-Cache", source)
	w.Header().Set("X-DeepSeek-Web-To-API-Cache-Expires-At", entry.DiskExpiresAt.UTC().Format(time.RFC3339))
	if entry.Status == 0 {
		entry.Status = http.StatusOK
	}
	w.WriteHeader(entry.Status)
	if len(entry.Body) > 0 {
		if _, err := w.Write(entry.Body); err != nil {
			config.Logger.Warn("[response_cache] replay write failed", "error", err)
		}
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (e Entry) cacheable(maxBody int64) bool {
	return e.uncacheableReason(maxBody) == ""
}

func (e Entry) uncacheableReason(maxBody int64) string {
	if e.Status < 200 || e.Status >= 300 {
		return "status_non_2xx"
	}
	if len(e.Body) == 0 {
		return "empty_body"
	}
	if maxBody > 0 && int64(len(e.Body)) > maxBody {
		return "oversized_response"
	}
	if hasHeaderToken(e.Header.Get("Cache-Control"), "no-store") {
		return "response_no_store"
	}
	if len(e.Header.Values("Set-Cookie")) > 0 {
		return "set_cookie"
	}
	return ""
}

func (e Entry) memorySize() int64 {
	size := int64(len(e.Body))
	for k, vv := range e.Header {
		size += int64(len(k))
		for _, v := range vv {
			size += int64(len(v))
		}
	}
	return size
}

type captureResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
	maxBody     int64
	truncated   bool
}

func newCaptureResponseWriter(w http.ResponseWriter, maxBody int64) *captureResponseWriter {
	return &captureResponseWriter{ResponseWriter: w, maxBody: maxBody}
}

func (w *captureResponseWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.capture(p)
	return w.ResponseWriter.Write(p)
}

func (w *captureResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(w, r)
}

func (w *captureResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *captureResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (w *captureResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *captureResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *captureResponseWriter) capture(p []byte) {
	if w.truncated || len(p) == 0 {
		return
	}
	if w.maxBody > 0 && int64(w.body.Len()+len(p)) > w.maxBody {
		remaining := int(w.maxBody) - w.body.Len()
		if remaining > 0 {
			_, _ = w.body.Write(p[:remaining])
		}
		w.truncated = true
		return
	}
	_, _ = w.body.Write(p)
}

func (w *captureResponseWriter) cacheEntry() (Entry, bool, string) {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	entry := Entry{
		Status: status,
		Header: sanitizeResponseHeader(w.Header()),
		Body:   append([]byte(nil), w.body.Bytes()...),
	}
	if w.truncated {
		return Entry{}, false, "oversized_response"
	}
	if reason := entry.uncacheableReason(w.maxBody); reason != "" {
		return Entry{}, false, reason
	}
	return entry, true, ""
}

func requestForcesBypass(r *http.Request) bool {
	if r == nil {
		return false
	}
	cacheControl := r.Header.Get("Cache-Control")
	if hasHeaderToken(cacheControl, "no-cache") || hasHeaderToken(cacheControl, "no-store") {
		return true
	}
	if strings.EqualFold(requestHeaderValue(r.Header, "X-DeepSeek-Web-To-API-Cache-Control", "X-Ds2-Cache-Control"), "bypass") {
		return true
	}
	return false
}

func hasHeaderToken(raw, token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	for _, part := range strings.Split(raw, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == token {
			return true
		}
	}
	return false
}

type varyRequestHeader struct {
	Name   string
	Legacy []string
}

func varyRequestHeaders() []varyRequestHeader {
	return []varyRequestHeader{
		{Name: "Accept"},
		{Name: "Anthropic-Beta"},
		{Name: "Anthropic-Version"},
		{Name: "Content-Type"},
		{Name: "X-DeepSeek-Web-To-API-Session-Affinity-Key", Legacy: []string{"X-Ds2-Session-Affinity-Key", "X-OpenClaw-Session-Id", "X-Client-Request-Id", "X-Session-Affinity", "Session-Id"}},
		{Name: "X-DeepSeek-Web-To-API-Source", Legacy: []string{"X-Ds2-Source"}},
		{Name: "X-DeepSeek-Web-To-API-Target-Account", Legacy: []string{"X-Ds2-Target-Account"}},
	}
}

func requestHeaderValue(h http.Header, primary string, legacy ...string) string {
	if value := strings.TrimSpace(h.Get(primary)); value != "" {
		return value
	}
	for _, name := range legacy {
		if value := strings.TrimSpace(h.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func requestHeaderValues(h http.Header, primary string, legacy ...string) []string {
	if values := h.Values(primary); len(values) > 0 {
		return values
	}
	for _, name := range legacy {
		if values := h.Values(name); len(values) > 0 {
			return values
		}
	}
	return nil
}

func writeKeyPart(w io.Writer, value string) {
	_, _ = io.WriteString(w, value)
	_, _ = w.Write([]byte{0})
}

func canonicalQuery(values map[string][]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		vals := append([]string(nil), values[key]...)
		sort.Strings(vals)
		parts = append(parts, key+"="+strings.Join(vals, "\x1f"))
	}
	return strings.Join(parts, "&")
}

func sanitizeResponseHeader(h http.Header) http.Header {
	out := http.Header{}
	for k, vv := range h {
		if skipResponseHeader(k) {
			continue
		}
		for _, v := range vv {
			out.Add(k, v)
		}
	}
	return out
}

func skipResponseHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "content-length", "date", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
		"x-deepseek-web-to-api-cache", "x-deepseek-web-to-api-cache-expires-at",
		"x-ds2-cache", "x-ds2-cache-expires-at":
		return true
	default:
		return false
	}
}

func cloneEntry(in Entry) Entry {
	return Entry{
		Status:        in.Status,
		Header:        cloneHeader(in.Header),
		Body:          append([]byte(nil), in.Body...),
		DiskExpiresAt: in.DiskExpiresAt,
	}
}

func cloneHeader(in http.Header) http.Header {
	if in == nil {
		return http.Header{}
	}
	out := make(http.Header, len(in))
	for k, vv := range in {
		out[k] = append([]string(nil), vv...)
	}
	return out
}

func normalizeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if len(key) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(key); err != nil {
		return ""
	}
	return key
}

func (c *Cache) Stats() map[string]any {
	if c == nil {
		return map[string]any{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lookups := c.hits + c.misses
	cacheableLookups := c.hits + c.stores
	cacheableMisses := c.stores
	uncacheableTotal := int64(0)
	for _, count := range c.uncacheable {
		uncacheableTotal += count
	}
	out := map[string]any{
		"lookups":            lookups,
		"hits":               c.hits,
		"misses":             c.misses,
		"stores":             c.stores,
		"cacheable_lookups":  cacheableLookups,
		"cacheable_misses":   cacheableMisses,
		"uncacheable_misses": uncacheableTotal,
		"memory_hits":        c.memoryHits,
		"disk_hits":          c.diskHits,
		"memory_items":       len(c.items),
		"memory_bytes":       c.memoryBytes,
		"memory_max_bytes":   c.memoryMaxBytes,
		"memory_ttl_seconds": int(c.memoryTTL.Seconds()),
		"disk_ttl_seconds":   int(c.diskTTL.Seconds()),
		"disk_max_bytes":     c.diskMaxBytes,
		"disk_dir":           c.dir,
		"compression":        "gzip",
	}
	for reason, count := range c.uncacheable {
		out["uncacheable_"+reason] = count
	}
	return out
}

func (c *Cache) String() string {
	if c == nil {
		return "response cache disabled"
	}
	return fmt.Sprintf("response cache memory=%s memory_max=%d disk=%s disk_max=%d dir=%s compression=gzip", c.memoryTTL, c.memoryMaxBytes, c.diskTTL, c.diskMaxBytes, c.dir)
}
