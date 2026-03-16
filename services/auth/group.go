// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"net/http"
	"strings"

	user_model "code.gitea.io/gitea/models/user"
)

// Ensure the struct implements the interface.
var (
	_ Method = &Group{}
)

// Group implements the Auth interface with serval Auth.
// 【読み順 STEP 3】複数の認証方式を合成するコンポジットパターン。
// Web 用と API 用で異なる認証グループを構成できる。
// 例: API 用 → NewGroup(&OAuth2{}, &HTTPSign{}, &Basic{})
//     Web 用 → NewGroup(&Session{}) + 条件付きで OAuth2, Basic, ReverseProxy を追加
type Group struct {
	methods []Method
}

// NewGroup creates a new auth group
func NewGroup(methods ...Method) *Group {
	return &Group{
		methods: methods,
	}
}

// Add adds a new method to group
func (b *Group) Add(method Method) {
	b.methods = append(b.methods, method)
}

// Name returns group's methods name
func (b *Group) Name() string {
	names := make([]string, 0, len(b.methods))
	for _, m := range b.methods {
		names = append(names, m.Name())
	}
	return strings.Join(names, ",")
}

// Verify は登録された認証方式を順番に試し、最初に成功したユーザーを返す。
// 【読み順 STEP 3 続き】認証チェーンの核心ロジック:
//   1. 各 Method.Verify() を順に呼ぶ
//   2. user が返れば即座に成功（以降の方式はスキップ）
//   3. error が返っても次の方式を試す（同じヘッダを複数方式が読む場合がある）
//   4. 全方式で user が見つからなければ、最初のエラーを返す
func (b *Group) Verify(req *http.Request, w http.ResponseWriter, store DataStore, sess SessionStore) (*user_model.User, error) {
	// Try to sign in with each of the enabled plugins
	var retErr error
	for _, m := range b.methods {
		user, err := m.Verify(req, w, store, sess)
		if err != nil {
			if retErr == nil {
				retErr = err
			}
			// Try other methods if this one failed.
			// Some methods may share the same protocol to detect if they are matched.
			// For example, OAuth2 and conan.Auth both read token from "Authorization: Bearer <token>" header,
			// If OAuth2 returns error, we should give conan.Auth a chance to try.
			continue
		}

		// If any method returns a user, we can stop trying.
		// Return the user and ignore any error returned by previous methods.
		if user != nil {
			if store.GetData()["AuthedMethod"] == nil {
				store.GetData()["AuthedMethod"] = m.Name()
			}
			return user, nil
		}
	}

	// If no method returns a user, return the error returned by the first method.
	return nil, retErr
}
