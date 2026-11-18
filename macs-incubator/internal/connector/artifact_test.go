// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"macs.local/tower/agentmsg"
)

// V1.5 Task 7：本地路径与非法 digest 拒绝（设计 §7.3）
func TestValidateArtifactRefRejectsLocalPathAndBadDigest(t *testing.T) {
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "/Users/mac/shot.png", Digest: "sha256:abc", Kind: "screenshot", SizeBytes: 10}); err == nil {
		t.Fatal("local path accepted as shared artifact")
	}
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "s3://bucket/obj", Digest: "not-a-sha256", Kind: "log", SizeBytes: 10}); err == nil {
		t.Fatal("invalid digest accepted")
	}
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "https://store/obj", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "image", SizeBytes: 1024}); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	// file:// 与相对路径拒绝
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "file:///tmp/x", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "log"}); err == nil {
		t.Fatal("file:// accepted")
	}
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "relative/path.bin", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "log"}); err == nil {
		t.Fatal("relative path accepted")
	}
	// 非回环 http 拒绝
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "http://store.example.com/obj", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "log"}); err == nil {
		t.Fatal("non-loopback http accepted")
	}
	// 回环 http 放行（开发/测试共享存储）
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "http://127.0.0.1:8080/obj", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "log"}); err != nil {
		t.Fatalf("loopback http rejected: %v", err)
	}
}

// V1.5 Task 7：下载校验 digest（设计 §7.2，不一致拒绝）
func TestDownloadVerifiesDigest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer server.Close()
	// digest of "hello" is 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	good := agentmsg.ArtifactRef{URI: server.URL, Digest: "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", Kind: "log"}
	if _, err := DownloadAndVerify(context.Background(), good); err != nil {
		t.Fatalf("matching digest rejected: %v", err)
	}
	bad := agentmsg.ArtifactRef{URI: server.URL, Digest: "sha256:" + strings.Repeat("0", 64), Kind: "log"}
	if _, err := DownloadAndVerify(context.Background(), bad); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}

// V1.5 Task 7：上传生成共享 URI + 正确 digest（设计 §7.1）
func TestUploadArtifactProducesValidRef(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing auth header")
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	data := []byte(`{"image":"layers"}`)
	ref, err := UploadArtifact(context.Background(), data, "image",
		StorageConfig{UploadURL: server.URL + "/objects/{{name}}", UploadHeaders: map[string]string{"Authorization": "Bearer tok"}},
		"attempt-1/image")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.URI, server.URL+"/objects/") || ref.Kind != "image" || int64(len(data)) != ref.SizeBytes {
		t.Fatalf("ref: %+v", ref)
	}
	if ref.Digest != sha256Digest(data) {
		t.Fatalf("digest mismatch: %s", ref.Digest)
	}
	if string(gotBody) != string(data) {
		t.Fatal("uploaded body mismatch")
	}
}

// 未配置共享存储 → 上传明确失败（不得静默用本地路径替代）
func TestUploadArtifactWithoutStorageFails(t *testing.T) {
	if _, err := UploadArtifact(context.Background(), []byte("x"), "log", StorageConfig{}, "n"); err == nil {
		t.Fatal("upload without storage must fail")
	}
}

// 本地路径判定
func TestIsLocalArtifactPath(t *testing.T) {
	local := []string{"/Users/mac/a.zip", "C:\\tmp\\a.zip", "file:///tmp/a", "rel/path", "a.zip"}
	for _, p := range local {
		if !IsLocalArtifactPath(p) {
			t.Errorf("%q should be local", p)
		}
	}
	shared := []string{"s3://b/o", "oci://reg/img:tag", "https://s/o", "http://127.0.0.1:9/o"}
	for _, p := range shared {
		if IsLocalArtifactPath(p) {
			t.Errorf("%q should not be local", p)
		}
	}
}
