# 認証・認可 コードリーディングガイド

コード内の `【読み順 STEP N】` コメントに対応するガイド。

## 読む順番

以下の順でコードを追うと、認証（誰か）→ 認可（何ができるか）→ 適用（許可するか）の流れで理解できる。

### フェーズ 1: 権限の定義

| STEP | ファイル | 何がわかるか |
|------|---------|-------------|
| 1 | [`models/perm/access_mode.go`](../../models/perm/access_mode.go) | AccessMode の 5 段階定義（None→Read→Write→Admin→Owner）。int 型なので `>=` で比較可能 |

### フェーズ 2: 認証の仕組み

| STEP | ファイル | 何がわかるか |
|------|---------|-------------|
| 2 | [`services/auth/interface.go`](../../services/auth/interface.go) | Method インターフェース。戻り値の 3 パターン規約（成功/判定不可/失敗） |
| 3 | [`services/auth/group.go`](../../services/auth/group.go) | 複数認証方式の合成（コンポジットパターン）。認証チェーンのループ処理 |
| 4a | [`services/auth/session.go`](../../services/auth/session.go) | 最もシンプルな Method 実装。セッションから uid を取得するだけ |
| 4b | [`services/auth/basic.go`](../../services/auth/basic.go) | 複雑な Method 実装。OAuth2→PAT→ActionToken→Password→2FA の順で試行 |
| 5 | [`routers/common/auth.go`](../../routers/common/auth.go) | AuthShared: 認証結果をコンテキストに書き込む共通処理 |

### フェーズ 3: 認可の仕組み

| STEP | ファイル | 何がわかるか |
|------|---------|-------------|
| 6 | [`models/perm/access/repo_permission.go`](../../models/perm/access/repo_permission.go) (Permission) | リポジトリ権限 + Unit 単位権限の構造体。CanRead/CanWrite/IsAdmin メソッド |
| 6+ | [`models/perm/access/access.go`](../../models/perm/access/access.go) | Access テーブル（権限キャッシュ）と再計算ロジック |
| 7 | [`models/perm/access/repo_permission.go`](../../models/perm/access/repo_permission.go) (GetUserRepoPermission) | 権限解決の核心。匿名→Owner→Admin→コラボ→チームの優先順で決定 |
| 7+ | [`models/organization/team.go`](../../models/organization/team.go) | チーム構造体。Unit ごとの権限オーバーライド |

### フェーズ 4: 認可の適用

| STEP | ファイル | 何がわかるか |
|------|---------|-------------|
| 8 | [`services/context/permission.go`](../../services/context/permission.go) | 認可ミドルウェア。RequireRepoAdmin, RequireUnitWriter, RequireUnitReader |
| 9 | [`services/context/context.go`](../../services/context/context.go) | Web Context。Doer（認証ユーザー）と Repo.Permission（権限）が集約される場所 |
| 9+ | [`services/context/api.go`](../../services/context/api.go) | API Context。Web Context と同じ構造だがテンプレート関連がない |

## 処理フロー

```
リクエスト受信
    │
    ▼
① STEP 2-4: Group.Verify() で認証
    │  methods を順に試行 → 最初に成功したユーザーを採用
    ▼
② STEP 5: AuthShared() で ctx.Data にユーザー情報セット
    │  → ctx.Doer, ctx.IsSigned が設定される
    ▼
③ STEP 6-7: GetUserRepoPermission() で権限解決
    │  匿名 → Owner → Admin → access テーブル → チーム権限集計
    ▼
④ STEP 8: ミドルウェアで認可チェック
    │  RequireRepoAdmin() / RequireUnitWriter() → 不足なら 404
    ▼
⑤ STEP 9: ハンドラ実行（ctx.Doer, ctx.Repo.CanWrite() 等で追加チェック可）
```

## 設計パターン

### 1. プラガブル認証（Strategy + Composite）
- `Method` インターフェースで各認証方式を差し替え可能
- `Group` で複数方式を合成し、Web/API で異なるグループを構成

### 2. 段階的 AccessMode（int 比較）
- `if mode >= AccessModeWrite` で「Write 以上の権限があるか」を一発判定
- 新しいロールを追加するときも既存コードに影響しない

### 3. 権限キャッシュ（Access テーブル）
- コラボレータ・チーム権限の最大値を事前計算して DB に保存
- 権限変更時に `RecalculateAccesses()` で再構築

### 4. Unit 単位の細粒度制御
- チームに「Code は Write、Issues は Read」のような設定が可能
- `Permission.unitsMode` マップで管理

### 5. Context への集約
- 認証結果（Doer）と認可結果（Permission）が Context に集約
- ハンドラからは `ctx.Repo.CanWrite(unit.TypeCode)` のように直感的にアクセス

## 関連ファイル（発展的な読み物）

| ファイル | 内容 |
|---------|------|
| [`models/auth/source.go`](../../models/auth/source.go) | 認証ソース（LDAP, OAuth2, SAML 等）の登録・管理 |
| [`services/auth/signin.go`](../../services/auth/signin.go) | パスワード認証の実装。DB → 外部ソース順に試行 |
| [`services/auth/oauth2.go`](../../services/auth/oauth2.go) | OAuth2 トークン認証 |
| [`routers/web/web.go`](../../routers/web/web.go) | Web ルートの認証グループ構成 |
| [`routers/api/v1/api.go`](../../routers/api/v1/api.go) | API ルートの認証グループ構成 |
| [`models/organization/team_repo.go`](../../models/organization/team_repo.go) | チーム × リポジトリの関連付け |
