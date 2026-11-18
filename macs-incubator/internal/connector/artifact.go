// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// artifact.go V1.5 跨机制品（设计 §7）
// 跨机制品 URI 只允许 s3:// oci:// https://；本地路径不得进入其他 Connector 的 input_refs。
// 制品上传失败时 attempt 标记 failed，不得以本地路径替代。
// 共享存储访问凭据只存在于 Connector 本机配置，不进塔。
package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"macs.local/tower/agentmsg"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// isLoopbackHost http 仅放行回环地址（开发/测试用 httptest 共享存储）
func isLoopbackHost(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// ValidateArtifactRef 引用校验（Task 7 契约）：
//   - URI scheme ∈ {s3, oci, https}——本地路径/相对路径/file:// 一律拒绝
//   - digest 必须是 sha256:<64 hex>
//   - kind 非空
func ValidateArtifactRef(r agentmsg.ArtifactRef) error {
	uri := strings.ToLower(strings.TrimSpace(r.URI))
	scheme, _, ok := strings.Cut(uri, "://")
	if !ok {
		return fmt.Errorf("artifact uri missing scheme: %q", r.URI)
	}
	switch scheme {
	case "s3", "oci", "https":
	case "http":
		if !isLoopbackHost(r.URI) {
			return fmt.Errorf("artifact uri scheme %q not allowed (s3/oci/https only; http 仅限本机回环开发)", scheme)
		}
	default:
		return fmt.Errorf("artifact uri scheme %q not allowed (s3/oci/https only)", scheme)
	}
	if strings.Contains(r.URI, "//./") || strings.HasPrefix(r.URI, "https:///") || strings.HasPrefix(r.URI, "s3:///") {
		return fmt.Errorf("artifact uri host required: %q", r.URI)
	}
	if !digestPattern.MatchString(strings.ToLower(r.Digest)) {
		return fmt.Errorf("artifact digest must be sha256:<64 hex>, got %q", r.Digest)
	}
	if strings.TrimSpace(r.Kind) == "" {
		return fmt.Errorf("artifact kind required")
	}
	if r.SizeBytes < 0 {
		return fmt.Errorf("artifact size negative")
	}
	return nil
}

// sha256Digest 计算内容摘要
func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DownloadAndVerify 下载共享制品并校验 SHA-256（设计 §7.2）：
// 与声明不一致则拒绝执行（返回错误，调用方据此 failed）。
func DownloadAndVerify(ctx context.Context, r agentmsg.ArtifactRef) ([]byte, error) {
	uri := strings.ToLower(r.URI)
	if !strings.HasPrefix(uri, "https://") && !strings.HasPrefix(uri, "http://") {
		return nil, fmt.Errorf("artifact download only supports http(s), got %q", r.URI)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, r.URI, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("artifact download %s: %w", r.URI, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artifact download %s: status %d", r.URI, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, err
	}
	if got := sha256Digest(data); got != strings.ToLower(r.Digest) {
		return nil, fmt.Errorf("artifact digest mismatch for %s: expected %s got %s", r.URI, r.Digest, got)
	}
	return data, nil
}

// UploadArtifact 上传制品到共享存储（设计 §7.1）：PUT 数据 → 返回带 digest 的共享引用。
// 上传失败返回错误（调用方将 attempt 标记 failed，不得以本地路径替代）。
func UploadArtifact(ctx context.Context, data []byte, kind string, storage StorageConfig, name string) (agentmsg.ArtifactRef, error) {
	if storage.UploadURL == "" {
		return agentmsg.ArtifactRef{}, fmt.Errorf("artifact upload: shared storage not configured")
	}
	if name == "" {
		name = fmt.Sprintf("%d-artifact.bin", time.Now().UnixMilli())
	}
	name = filepath.Base(name) // 防路径穿越
	uri := strings.ReplaceAll(storage.UploadURL, "{{name}}", name)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPut, uri, bytes.NewReader(data))
	if err != nil {
		return agentmsg.ArtifactRef{}, err
	}
	for k, v := range storage.UploadHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return agentmsg.ArtifactRef{}, fmt.Errorf("artifact upload %s: %w", uri, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return agentmsg.ArtifactRef{}, fmt.Errorf("artifact upload %s: status %d", uri, resp.StatusCode)
	}
	ref := agentmsg.ArtifactRef{
		URI:       uri,
		Digest:    sha256Digest(data),
		Kind:      kind,
		SizeBytes: int64(len(data)),
	}
	if err := ValidateArtifactRef(ref); err != nil {
		return agentmsg.ArtifactRef{}, err
	}
	return ref, nil
}

// IsLocalArtifactPath 本地路径判定（file://、绝对路径、UNC/盘符）——
// 这类引用禁止进入跨机 input_refs，必须先上传共享存储
func IsLocalArtifactPath(uri string) bool {
	u := strings.ToLower(strings.TrimSpace(uri))
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "file://") {
		return true
	}
	if strings.HasPrefix(u, "/") || strings.HasPrefix(u, "\\") {
		return true
	}
	if len(u) >= 2 && u[1] == ':' && u[0] >= 'a' && u[0] <= 'z' { // C:\ / c:/
		return true
	}
	scheme, _, ok := strings.Cut(u, "://")
	if !ok {
		return true // 无 scheme 视为本地/相对路径
	}
	return scheme != "s3" && scheme != "oci" && scheme != "https" && scheme != "http"
}
