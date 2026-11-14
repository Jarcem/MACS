// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package trace

import (
	"strings"
	"testing"
)

// §12.1 单元测试：6.4.4 指纹链计算（无父 / 有父 / 防篡改可检出）
func TestChainFingerprint(t *testing.T) {
	noParent := ChainFingerprint("hello", "")
	if !strings.HasPrefix(noParent, "sha256:") {
		t.Errorf("指纹应带 sha256: 前缀，got %s", noParent)
	}
	if noParent != ChainFingerprint("hello", "") {
		t.Error("同内容指纹应确定")
	}
	if noParent == ChainFingerprint("hello!", "") {
		t.Error("内容变化指纹必须变化（防篡改前提）")
	}
	withParent := ChainFingerprint("hello", noParent)
	if withParent == noParent {
		t.Error("父链不同则指纹不同")
	}
	// 与裸 sha256(content) 不同（链式）
	if withParent == ChainFingerprint("hello", "") {
		t.Error("有父链指纹应不同于无父指纹")
	}
}

// 篡改检测：父链任一环节变化，下游指纹全部失效（追溯链核心断言）
func TestTamperDetection(t *testing.T) {
	root := ChainFingerprint("root", "")
	childA := ChainFingerprint("artifact-A", root)
	childB := ChainFingerprint("artifact-B", childA)
	tamperedRoot := ChainFingerprint("root-TAMPERED", "")
	if ChainFingerprint("artifact-A", tamperedRoot) == childA {
		t.Error("篡改根节点应改变子指纹（E06 可检出）")
	}
	_ = childB
}
