// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package perm

import (
	"fmt"
	"slices"

	"code.gitea.io/gitea/modules/util"
)

// AccessMode specifies the users access mode
// 【読み順 STEP 1】認証・認可の出発点。int 型なので大小比較（>=）で権限チェックできる。
// 例: if userMode >= AccessModeWrite { /* 書き込み許可 */ }
type AccessMode int

const (
	AccessModeNone AccessMode = iota // 0: アクセス不可 — 未認証ユーザーまたは権限なし

	AccessModeRead  // 1: 読み取り — 公開リポジトリの匿名アクセスはここ
	AccessModeWrite // 2: 書き込み — コラボレータのデフォルト権限
	AccessModeAdmin // 3: 管理者 — リポジトリ設定の変更が可能
	AccessModeOwner // 4: オーナー — リポジトリ所有者・サイト管理者・ Owners チームメンバー
)

// ToString returns the string representation of the access mode, do not make it a Stringer, otherwise it's difficult to render in templates
func (mode AccessMode) ToString() string {
	switch mode {
	case AccessModeRead:
		return "read"
	case AccessModeWrite:
		return "write"
	case AccessModeAdmin:
		return "admin"
	case AccessModeOwner:
		return "owner"
	default:
		return "none"
	}
}

func (mode AccessMode) LogString() string {
	return fmt.Sprintf("<AccessMode:%d:%s>", mode, mode.ToString())
}

// ParseAccessMode returns corresponding access mode to given permission string.
func ParseAccessMode(permission string, allowed ...AccessMode) AccessMode {
	m := AccessModeNone
	switch permission {
	case "read":
		m = AccessModeRead
	case "write":
		m = AccessModeWrite
	case "admin":
		m = AccessModeAdmin
	default:
		// the "owner" access is not really used for user input, it's mainly for checking access level in code, so don't parse it
	}
	if len(allowed) == 0 {
		return m
	}
	return util.Iif(slices.Contains(allowed, m), m, AccessModeNone)
}

// ErrInvalidAccessMode is returned when an invalid access mode is used
var ErrInvalidAccessMode = util.NewInvalidArgumentErrorf("Invalid access mode")
