// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"net/http"

	actions_model "code.gitea.io/gitea/models/actions"
	auth_model "code.gitea.io/gitea/models/auth"
	user_model "code.gitea.io/gitea/models/user"
	"code.gitea.io/gitea/modules/auth/httpauth"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/setting"
	"code.gitea.io/gitea/modules/timeutil"
	"code.gitea.io/gitea/modules/util"
)

// 【読み順 STEP 4b】コンパイル時に Basic が Method インターフェースを実装していることを保証する。
// 実装漏れがあればここでコンパイルエラーになる（Go の慣用パターン）。
var (
	_ Method = &Basic{}
)

// 認証方式を識別する定数群。
// store.GetData()["LoginMethod"] にセットされ、後続の処理で「どの方式で認証されたか」を判定するのに使われる。
// 例: GetAccessScope() で LoginMethod に応じてトークンスコープを決定する。
const (
	BasicMethodName       = "basic"        // ユーザー名 + パスワードによる Basic 認証
	AccessTokenMethodName = "access_token" // パーソナルアクセストークン (PAT) による認証
	OAuth2TokenMethodName = "oauth2_token" // OAuth2 アクセストークンによる認証
	ActionTokenMethodName = "action_token" // GitHub Actions 互換のタスクトークンによる認証
)

// Basic は Authorization ヘッダの Basic 認証データを解析して認証を行う Method 実装。
// API リクエスト専用。以下の順で認証を試みる:
//
//	1. OAuth2 アクセストークン → token テーブルから UID を取得
//	2. パーソナルアクセストークン (PAT) → SHA ハッシュで検索
//	3. GitHub Actions タスクトークン → 実行中タスクから検索
//	4. ユーザー名/パスワード → UserSignIn() で DB/LDAP 等の認証ソースを順に試行
//	5. 2FA チェック（WebAuthn 登録済み → 拒否、TOTP → X-Gitea-OTP ヘッダで検証）
//
// 【読み順 STEP 4b】session.go (STEP 4a) と比較すると、複数の認証戦略をフォールスルーで
// 試行する複雑なパターンがわかる。token 系は Verify の前半、パスワードは後半で処理される。
type Basic struct{}

// Name は認証方式の識別名を返す。
// Group (STEP 3) がログ出力やデバッグで使用する。
func (b *Basic) Name() string {
	return BasicMethodName
}

// parseAuthBasic は Authorization ヘッダから Basic 認証の情報を解析する。
//
// Basic 認証のトークン判定ロジック:
//   - パスワードが空 or "x-oauth-basic" → ユーザー名をトークンとして扱う
//     （GitHub 互換: トークンをユーザー名フィールドに入れる方式）
//   - それ以外 → パスワードをトークンとして扱う
//     （ユーザー名は通常のユーザー名、パスワード欄にトークンを入れる方式）
//
// 戻り値の authToken は後続の VerifyAuthToken() でトークン認証に使われる。
// uname/passwd はトークン認証が全て失敗した場合のパスワード認証で使われる。
func (b *Basic) parseAuthBasic(req *http.Request) (ret struct{ authToken, uname, passwd string }) {
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		return ret
	}
	parsed, ok := httpauth.ParseAuthorizationHeader(authHeader)
	if !ok || parsed.BasicAuth == nil {
		return ret
	}
	uname, passwd := parsed.BasicAuth.Username, parsed.BasicAuth.Password

	// パスワードが空 or "x-oauth-basic" なら、ユーザー名がトークンそのもの
	isUsernameToken := len(passwd) == 0 || passwd == "x-oauth-basic"
	// デフォルトではユーザー名をトークンとみなす
	authToken := uname
	if !isUsernameToken {
		log.Trace("Basic Authorization: Attempting login for: %s", uname)
		// パスワード欄にトークンが入っているパターン
		authToken = passwd
	} else {
		log.Trace("Basic Authorization: Attempting login with username as token")
	}
	ret.authToken, ret.uname, ret.passwd = authToken, uname, passwd
	return ret
}

// VerifyAuthToken はトークン文字列のみで認証を試みる。
// 他の認証方式（OAuth2 等）からトークン検証ロジックを再利用するために公開されている。
//
// 試行順序:
//  1. OAuth2 アクセストークン → token テーブルから UID を引き、ユーザーを返す
//  2. パーソナルアクセストークン (PAT) → SHA でトークンを検索、最終使用日時を更新して返す
//  3. Actions タスクトークン → 実行中タスクに紐づく Actions 用仮想ユーザーを返す
//
// いずれにもマッチしなければ (nil, nil) を返す（Method インターフェースの「判定不可」規約）。
func (b *Basic) VerifyAuthToken(req *http.Request, w http.ResponseWriter, store DataStore, sess SessionStore, authToken string) (*user_model.User, error) {
	// --- 試行 1: OAuth2 アクセストークン ---
	// トークン文字列から OAuth2 スコープとユーザー ID を取得する
	_, uid := GetOAuthAccessTokenScopeAndUserID(req.Context(), authToken)
	if uid != 0 {
		log.Trace("Basic Authorization: Valid OAuthAccessToken for user[%d]", uid)

		u, err := user_model.GetUserByID(req.Context(), uid)
		if err != nil {
			log.Error("GetUserByID:  %v", err)
			return nil, err
		}

		store.GetData()["LoginMethod"] = OAuth2TokenMethodName
		store.GetData()["IsApiToken"] = true
		return u, nil
	}

	// --- 試行 2: パーソナルアクセストークン (PAT) ---
	// トークンの SHA ハッシュで access_token テーブルを検索する
	token, err := auth_model.GetAccessTokenBySHA(req.Context(), authToken)
	if err == nil {
		log.Trace("Basic Authorization: Valid AccessToken for user[%d]", uid)
		u, err := user_model.GetUserByID(req.Context(), token.UID)
		if err != nil {
			log.Error("GetUserByID:  %v", err)
			return nil, err
		}

		// トークンの最終使用日時を更新（トークンのアクティビティ追跡用）
		token.UpdatedUnix = timeutil.TimeStampNow()
		if err = auth_model.UpdateAccessToken(req.Context(), token); err != nil {
			log.Error("UpdateAccessToken:  %v", err)
		}

		store.GetData()["LoginMethod"] = AccessTokenMethodName
		store.GetData()["IsApiToken"] = true
		store.GetData()["ApiTokenScope"] = token.Scope // スコープを保存 → CheckRepoScopedToken() で参照
		return u, nil
	} else if !auth_model.IsErrAccessTokenNotExist(err) && !auth_model.IsErrAccessTokenEmpty(err) {
		// トークンが存在しない / 空の場合は次の試行へ進む（正常系）
		// それ以外のエラー（DB 障害等）はログに残す
		log.Error("GetAccessTokenBySha: %v", err)
	}

	// --- 試行 3: Actions タスクトークン ---
	// GitHub Actions 互換の CI/CD 実行タスクに紐づくトークンを検索する
	task, err := actions_model.GetRunningTaskByToken(req.Context(), authToken)
	if err == nil && task != nil {
		log.Trace("Basic Authorization: Valid AccessToken for task[%d]", task.ID)
		store.GetData()["LoginMethod"] = ActionTokenMethodName
		// タスク ID を持つ Actions 専用仮想ユーザーを返す
		// （実際の人間ユーザーではなく、CI が操作するための特殊ユーザー）
		return user_model.NewActionsUserWithTaskID(task.ID), nil
	}
	return nil, nil //nolint:nilnil // 全てのトークン認証が不適合 → 次の認証方式へ
}

// Verify は Method インターフェースの実装。Authorization ヘッダから認証情報を抽出し、
// 対応するユーザーを返す。
//
// 処理フロー:
//  1. parseAuthBasic() で Authorization ヘッダを解析
//  2. VerifyAuthToken() でトークン認証を試行（OAuth2 → PAT → Actions）
//  3. トークンで認証できなければ、パスワード認証を試行（setting.EnableBasicAuth が必要）
//  4. パスワード認証成功後、2FA チェック:
//     - WebAuthn 登録済み → Basic 認証では WebAuthn 検証できないため拒否
//     - TOTP 登録済み → X-Gitea-OTP ヘッダの値で検証
//
// 戻り値規約（Method インターフェースの契約、STEP 2 参照）:
//   - (user, nil):  認証成功
//   - (nil, nil):   この認証方式では判定不可 → Group が次の方式を試す
//   - (nil, error): 認証失敗（不正なパスワード、2FA 失敗等）→ Group がエラーを返す
func (b *Basic) Verify(req *http.Request, w http.ResponseWriter, store DataStore, sess SessionStore) (*user_model.User, error) {
	// ① Authorization ヘッダの解析
	parseBasicRet := b.parseAuthBasic(req)
	authToken, uname, passwd := parseBasicRet.authToken, parseBasicRet.uname, parseBasicRet.passwd
	if authToken == "" && uname == "" {
		return nil, nil //nolint:nilnil // Authorization ヘッダなし → この方式では判定不可
	}

	// ② トークン認証の試行（OAuth2 → PAT → Actions）
	u, err := b.VerifyAuthToken(req, w, store, sess, authToken)
	if u != nil || err != nil {
		return u, err
	}

	// ③ パスワード認証の試行
	// Basic 認証がサイト設定で無効化されている場合はスキップ
	if !setting.Service.EnableBasicAuth {
		return nil, nil //nolint:nilnil // Basic 認証無効 → この方式では判定不可
	}

	log.Trace("Basic Authorization: Attempting SignIn for %s", uname)
	// UserSignIn は DB → LDAP → SMTP 等の認証ソースを順に試行する（services/auth/signin.go）
	// source は認証ソースの情報で、2FA スキップ可否の判定に使用する
	u, source, err := UserSignIn(req.Context(), uname, passwd)
	if err != nil {
		if !user_model.IsErrUserNotExist(err) {
			log.Error("UserSignIn: %v", err)
		}
		return nil, err
	}

	// ④ 2FA (二要素認証) チェック
	// 認証ソースが 2FA スキップを許可している場合（例: 外部 IdP が既に 2FA 済み）はスキップ
	if !source.TwoFactorShouldSkip() {
		// WebAuthn（FIDO2 セキュリティキー）が登録されている場合、
		// Basic 認証では物理キーの検証ができないため拒否する
		hasWebAuthn, err := auth_model.HasWebAuthnRegistrationsByUID(req.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		if hasWebAuthn {
			return nil, ErrUserAuthMessage("basic authorization is not allowed while WebAuthn enrolled")
		}

		// TOTP（時間ベースワンタイムパスワード）の検証
		// X-Gitea-OTP ヘッダに OTP コードを含めて送信する必要がある
		if err := validateTOTP(req, u); err != nil {
			return nil, err
		}
	}

	store.GetData()["LoginMethod"] = BasicMethodName
	log.Trace("Basic Authorization: Logged in user %-v", u)

	return u, nil
}

// validateTOTP は TOTP（時間ベースワンタイムパスワード）の検証を行う。
//
//   - ユーザーが TOTP 未登録の場合 → nil を返す（2FA なしで通過）
//   - 登録済みの場合 → X-Gitea-OTP ヘッダの値を検証
//   - ヘッダなし or 不正な OTP → エラーを返す
func validateTOTP(req *http.Request, u *user_model.User) error {
	twofa, err := auth_model.GetTwoFactorByUID(req.Context(), u.ID)
	if err != nil {
		if auth_model.IsErrTwoFactorNotEnrolled(err) {
			// TOTP 未登録 → 2FA なしで認証成功
			return nil
		}
		return err
	}
	// X-Gitea-OTP ヘッダから OTP コードを取得して検証
	if ok, err := twofa.ValidateTOTP(req.Header.Get("X-Gitea-OTP")); err != nil {
		return err
	} else if !ok {
		return util.NewInvalidArgumentErrorf("invalid provided OTP")
	}
	return nil
}

// GetAccessScope は現在のリクエストの認証方式に応じたアクセススコープを返す。
//
// スコープの決定ロジック:
//   - ApiTokenScope が store にある場合 → PAT のスコープをそのまま返す（最も制限的）
//   - OAuth2 / Basic / PAT 認証 → AccessTokenScopeAll（全権限）
//   - Actions トークン / その他 → 空文字列（スコープなし）
//
// この関数は services/context/permission.go (STEP 8) の CheckRepoScopedToken() から呼ばれ、
// PAT のスコープが要求されたアクセスレベルを満たしているかの判定に使われる。
func GetAccessScope(store DataStore) auth_model.AccessTokenScope {
	// PAT に明示的なスコープが設定されている場合はそれを使用
	if v, ok := store.GetData()["ApiTokenScope"]; ok {
		return v.(auth_model.AccessTokenScope)
	}
	switch store.GetData()["LoginMethod"] {
	case OAuth2TokenMethodName:
		fallthrough
	case BasicMethodName, AccessTokenMethodName:
		// パスワード認証・ OAuth2 ・ PAT（スコープ未設定）は全権限
		return auth_model.AccessTokenScopeAll
	case ActionTokenMethodName:
		fallthrough
	default:
		// Actions トークンや未認証はスコープなし
		return ""
	}
}
