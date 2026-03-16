// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	// ユーザーモデル。DB のユーザーテーブルに対応する構造体を提供する
	user_model "code.gitea.io/gitea/models/user"
	// ミドルウェアユーティリティ。ロケール処理やコンテキストデータのキー定数を提供する
	"code.gitea.io/gitea/modules/web/middleware"
	// 認証サービス層。Verify() を持つ Method インターフェースや SessionStore を定義する
	auth_service "code.gitea.io/gitea/services/auth"
	// リクエストスコープのコンテキスト。ctx.Data（map[string]any）を通じてハンドラ間でデータを共有する
	"code.gitea.io/gitea/services/context"
)

// AuthResult は認証処理の結果を呼び出し元に返すための構造体。
// AuthShared() の戻り値として使われ、Web/API の各ルータが結果を受け取る。
type AuthResult struct {
	// Doer: 認証されたユーザー。未認証の場合は nil
	Doer *user_model.User
	// IsBasicAuth: Basic 認証（Authorization: Basic ヘッダ）で認証されたかどうか。
	// API のトークン認証と区別するために使われる
	IsBasicAuth bool
}

// 【読み順 STEP 5】認証結果をコンテキストに書き込む共通処理。
// Web/API 両方のルートから呼ばれる。認証成功時に以下をセット:
//   - ctx.Data["IsSigned"] = true
//   - ctx.Data["SignedUser"] = ユーザーオブジェクト
//   - ctx.Data["IsAdmin"] = 管理者フラグ
// この後、各ルートが ctx.Doer に代入してハンドラから参照可能にする。
//
// 引数:
//   - ctx:          リクエストスコープのコンテキスト。認証結果の書き込み先（ctx.Data）を持つ
//   - sessionStore: セッションストア。セッションベース認証で使用される
//   - authMethod:   認証メソッドのチェーン（Group）。Verify() を順番に試行して最初に成功したものを返す
//
// 戻り値:
//   - ar:  認証結果（ユーザー情報 + Basic 認証フラグ）
//   - err: 認証処理中に発生したエラー（認証失敗ではなく、内部エラー）
func AuthShared(ctx *context.Base, sessionStore auth_service.SessionStore, authMethod auth_service.Method) (ar AuthResult, err error) {
	// 認証メソッドチェーンの Verify() を呼び出す。
	// リクエストのヘッダ・セッション・トークンなどから認証を試みる。
	// 成功すれば *user_model.User が返り、未認証なら nil が返る。
	ar.Doer, err = authMethod.Verify(ctx.Req, ctx.Resp, ctx, sessionStore)
	if err != nil {
		// 内部エラー（DB エラーなど）が起きた場合は即座にエラーを返す
		return ar, err
	}

	// --- 認証成功（ユーザーが特定できた）場合 ---
	if ar.Doer != nil {
		// ユーザーの言語設定と現在のロケールが異なる場合、ロケールを再設定する。
		// 例: ユーザーが「日本語」設定だがブラウザの Accept-Language が「英語」の場合など
		if ctx.Locale.Language() != ar.Doer.Language {
			ctx.Locale = middleware.Locale(ctx.Resp, ctx.Req)
		}

		// Verify() が ctx.Data["AuthedMethod"] に認証方式名を書き込んでいるので、
		// それが "basic"（Basic 認証）だったかどうかを判定する。
		// 型アサーション .(string) で string 型に変換して比較している
		ar.IsBasicAuth = ctx.Data["AuthedMethod"].(string) == auth_service.BasicMethodName

		// --- コンテキストへの書き込み（ここが「認証結果をコンテキストに書き込む」の核心部分）---
		// 以降のハンドラやテンプレートから参照できるように、ctx.Data に認証情報をセットする

		// ログイン済みフラグ。テンプレートで {{if .IsSigned}} のように使われる
		ctx.Data["IsSigned"] = true
		// ユーザーオブジェクト本体。テンプレートやハンドラが .SignedUser でアクセスする
		ctx.Data[middleware.ContextDataKeySignedUser] = ar.Doer
		// ユーザー ID。数値での比較や DB 問い合わせに使われる
		ctx.Data["SignedUserID"] = ar.Doer.ID
		// 管理者フラグ。管理者専用ページの表示制御に使われる
		ctx.Data["IsAdmin"] = ar.Doer.IsAdmin
	} else {
		// --- 未認証（匿名アクセス）の場合 ---
		// SignedUserID を 0 にセットしておく。
		// テンプレートや後続処理で nil 参照を避けるためのデフォルト値
		ctx.Data["SignedUserID"] = int64(0)
	}

	// エラーなしで認証結果を返す
	return ar, nil
}

// VerifyOptions はルートごとの認証要件を定義する構造体。
// 各ハンドラの前段でチェックされ、条件を満たさない場合はリダイレクトや 403 を返す。
type VerifyOptions struct {
	// SignInRequired: true の場合、未ログインユーザーはログインページにリダイレクトされる
	SignInRequired bool
	// SignOutRequired: true の場合、ログイン済みユーザーはアクセスできない（ログインページなど）
	SignOutRequired bool
	// AdminRequired: true の場合、管理者権限がないと 403 エラーになる
	AdminRequired bool
	// DisableCrossOriginProtection: true の場合、CSRF 保護などのクロスオリジン保護を無効化する
	DisableCrossOriginProtection bool
}
