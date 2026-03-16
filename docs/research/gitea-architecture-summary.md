# Gitea アーキテクチャ調査まとめ

調査日: 2026-03-16

## 1. 全体概要

Gitea は Go 言語で書かれたセルフホスト型 Git ホスティングサービス。レイヤードアーキテクチャを採用したモノリシックなWebアプリケーションである。

### 技術スタック一覧

| 領域 | 技術 | 備考 |
|------|------|------|
| 言語 (バックエンド) | Go | |
| HTTPルーター | chi v5 (`go-chi/chi`) | 軽量ルーター、カスタムラッパーあり |
| ORM | xorm v1.3.11 | 複数DB対応 |
| CLI | urfave/cli v3 | サブコマンド構成 |
| 設定ファイル | INI形式 (`gopkg.in/ini.v1`) | `custom/conf/app.ini` |
| DB | SQLite3, MySQL, PostgreSQL, MSSQL | 4種対応 |
| フロントエンド | Vue 3.5 + TypeScript | SFC形式 |
| CSSフレームワーク | Fomantic UI + Tailwind CSS 3.4 | 3層CSS構成 |
| バンドラー | Webpack 5 | esbuild-loader で高速化 |
| テンプレートエンジン | Go `html/template` | サーバーサイドレンダリング |
| パッケージマネージャ | pnpm | |
| テスト (フロントエンド) | Vitest, Playwright (E2E) | |
| テスト (バックエンド) | Go標準テスト | |
| メトリクス | Prometheus | |
| セッション | chi-session | Redis/Memcache/Memory |

---

## 2. ディレクトリ構造

### トップレベル

```
gitea/
├── cmd/          CLI コマンド定義 (web, admin, hook, migrate 等)
├── models/       データモデル・DB エンティティ (ドメイン別)
├── services/     ビジネスロジック層
├── routers/      HTTPルーティング・ハンドラ
├── modules/      共有ユーティリティ・インフラモジュール (60+パッケージ)
├── templates/    Go テンプレート (サーバーサイド HTML)
├── web_src/      フロントエンドソース (JS/TS/CSS)
├── public/       静的アセット出力先
├── options/      デフォルトリソース (gitignore, ライセンス, ロケール)
├── tests/        テストユーティリティ・フィクスチャ
├── contrib/      Docker設定、スクリプト等
├── build/        ビルド成果物
├── tools/        開発ツール
├── docker/       Dockerファイル
└── custom/       カスタム設定テンプレート
```

---

## 3. バックエンドアーキテクチャ

### 3.1 レイヤー構成

```
Models (データ層)
  ↓
Services (ビジネスロジック層)
  ↓
Routers (HTTPハンドラ層)
  ↓
Templates (ビュー層)
```

### 3.2 エントリーポイント

- `main.go` → `cmd.NewMainApp()` で CLI アプリケーションを構築
- `cmd/web.go` (`CmdWeb`) が HTTP サーバーを起動 (デフォルト: ポート3000)
- `routers/init.go` の `NormalRoutes()` でルーティングツリーを構築

### 3.3 ルーティング構造

```
NormalRoutes()
├── ProtocolMiddlewares()  (グローバルミドルウェア)
├── Mount "/" → web_routers.Routes()         Web UI
├── Mount "/api/v1" → apiv1.Routes()         REST API v1
├── Mount "/api/internal" → private.Routes() 内部API
├── Mount "/api/packages" → packages API     パッケージレジストリ
├── Mount "/v2" → Container API              コンテナレジストリ
└── Mount "/api/actions" → Actions API       CI/CD
```

### 3.4 ミドルウェアチェーン

**グローバル (BeforeRouting):**
1. `chi_middleware.GetHead` - HEAD→GET リダイレクト
2. `ChiRoutePathHandler()` - パス正規化
3. `RequestContextHandler()` - リクエストコンテキスト + パニックリカバリ
4. `ForwardedHeadersHandler()` - リバースプロキシヘッダ処理
5. `routing.NewLoggerHandler()` - ルートログ
6. `context.AccessLogger()` - HTTPアクセスログ

**ルート適用後 (AfterRouting):**
1. Gzip圧縮 (`gzhttp`)
2. セッション管理 (`MustInitSessioner`)
3. コンテキスト生成 (`context.Contexter`)
4. 認証 (`newWebAuthMiddleware`) - OAuth2, Basic Auth, Session, ReverseProxy, SSPI 対応
5. グローバルデータ読込 (`PageGlobalData`)
6. レート制限 (`BlockExpensive`)
7. QoS制御

### 3.5 Models層 (`models/`)

ドメインごとにサブパッケージに分離:

| パッケージ | 内容 |
|-----------|------|
| `db/` | ORM エンジン初期化、DBコンテキスト、トランザクション |
| `repo/` | リポジトリ (コア、フォーク、リリース、ミラー等) |
| `issues/` | Issue、コメント、ラベル、マイルストーン |
| `pull/` | プルリクエスト |
| `user/` | ユーザーアカウント (27+ファイル) |
| `organization/` | 組織・チーム |
| `auth/` | 認証 |
| `asymkey/` | SSH/GPG 鍵 |
| `actions/` | CI/CD ワークフロー |
| `packages/` | パッケージレジストリ |
| `webhook/` | Webhook |
| `migrations/` | DBスキーママイグレーション (v1_10〜v1_22) |
| `project/` | プロジェクト管理 |
| `git/` | Gitメタデータ |
| `activities/` | アクティビティログ |
| `perm/` | 権限・アクセス制御 |
| `secret/` | シークレット管理 |
| `system/` | システムレベル設定 |

### 3.6 Services層 (`services/`)

ビジネスロジックを実装するサービス群:

| パッケージ | 内容 |
|-----------|------|
| `repository/` | リポジトリ操作 (作成、削除、採用等) |
| `issue/` | Issue管理 |
| `pull/` | PR管理 |
| `auth/` | 認証サービス |
| `user/` | ユーザーアカウント管理 |
| `org/` | 組織サービス |
| `git/` | Gitコマンド実行ラッパー |
| `gitdiff/` | Diff計算・レンダリング |
| `mailer/` | メール送信 (受信対応あり) |
| `webhook/` | Webhook発火 |
| `indexer/` | 全文検索インデックス (コード・Issue) |
| `mirror/` | リポジトリミラー同期 |
| `automerge/` | 自動マージ |
| `cron/` | Cronジョブスケジューリング |
| `notify/` | 通知ディスパッチ |
| `markup/` | Markdown/マークアップレンダリング |
| `convert/` | Models→APIレスポンス変換 |
| `packages/` | パッケージレジストリ |
| `actions/` | CI/CDサービス |
| `lfs/` | Git LFS |
| `migrations/` | リポジトリ移行 |
| `release/` | リリース管理 |
| `wiki/` | Wiki |
| `feed/` | Atom/RSSフィード |
| `oauth2_provider/` | OAuth2プロバイダ |
| `task/` | バックグラウンドタスク |

### 3.7 Modules層 (`modules/`)

共有インフラ・ユーティリティ (60+パッケージ):

**コアインフラ:**
- `setting/` - 設定管理
- `log/` - ログフレームワーク
- `cache/` - キャッシュ層
- `session/` - セッション管理
- `storage/` - ファイルストレージ抽象化 (S3, ローカル, OSS等)
- `queue/` - タスクキューイング
- `graceful/` - グレースフルシャットダウン

**Git関連:**
- `git/` - Git コマンドラッパー
- `gitrepo/` - Git リポジトリ操作
- `lfs/` / `lfstransfer/` - Git LFS

**Web関連:**
- `web/` - カスタムWebフレームワーク (chi ラッパー)
- `httplib/` - HTTPユーティリティ
- `structs/` - API DTO 定義

**認証・セキュリティ:**
- `auth/` - 認証ユーティリティ
- `ssh/` - SSH プロトコル
- `secret/` - シークレット暗号化/復号化

**検索:**
- `indexer/` - 全文検索 (Bleve バックエンド)

---

## 4. フロントエンドアーキテクチャ

### 4.1 ディレクトリ構造

```
web_src/
├── js/
│   ├── index.ts              メインエントリーポイント
│   ├── index-domready.ts     DOM ready 後の遅延ロード
│   ├── bootstrap.ts          初期化
│   ├── globals.ts            グローバル設定 (jQuery for Fomantic)
│   ├── components/           Vue 3 SFC コンポーネント (18ファイル)
│   ├── features/             ページ固有の機能モジュール (20+)
│   ├── modules/              再利用モジュール (fetch, toast等)
│   ├── webcomponents/        Web Components (軽量、head読込)
│   ├── utils/                ヘルパーユーティリティ
│   ├── render/               コンテンツレンダリング
│   ├── markup/               マークアップ処理
│   ├── standalone/           独立エントリーポイント (swagger, devtest)
│   └── vendor/               サードパーティ
├── css/
│   ├── index.css             メイン集約ファイル
│   ├── base.css, repo.css... ドメイン別CSS
│   ├── modules/              コンポーネントレベルCSS
│   ├── themes/               テーマ (light, dark, auto, アクセシビリティ)
│   ├── features/             機能固有CSS
│   └── markup/               マークアップCSS
├── fomantic/                 Fomantic UI カスタマイズ
└── svg/                      SVG アセット
```

### 4.2 フロントエンドとバックエンドの接続

1. **Go Router → テンプレートレンダリング**: `ctx.HTML(statusCode, templateName)`
2. **`window.config` オブジェクト** (`head_script.tmpl` で生成):
   - `appUrl`, `appSubUrl`, `assetUrlPrefix`
   - `pageData` (ページ固有データ)
   - `i18n` (ローカライゼーション)
   - Go ハンドラが `ctx.Data` / `ctx.PageData` で設定 → JS から参照
3. **Vue コンポーネント**: `window.config.pageData` からリアクティブにデータ取得
4. **API 通信**: `modules/fetch.ts` のカスタム fetch ラッパーで `/api/v1/` へ
5. **HTMX**: 動的HTML更新 (フルページリロード不要)
6. **data 属性**: HTML→JS へのデータ受け渡し

### 4.3 CSS 3層構成

1. **Fomantic UI** - ベースコンポーネントフレームワーク
2. **Tailwind CSS** - ユーティリティクラス (PostCSS経由)
3. **カスタムCSS** - Gitea固有のオーバーライド・機能CSS

### 4.4 主要UIライブラリ

- `chart.js` + `vue-chartjs` - グラフ
- `monaco-editor` - コードエディタ
- `mermaid` - ダイアグラム
- `katex` - 数式レンダリング
- `dropzone` - ファイルアップロード
- `sortablejs` - ドラッグ&ドロップ
- `easymde` - Markdownエディタ
- `swagger-ui-dist` - API ドキュメントUI

---

## 5. ビルドシステム

### 5.1 主要ビルドターゲット

```
make build              フロントエンド + バックエンド全体ビルド
make backend            Go バックエンドバイナリ
make frontend           Webpack アセットビルド
make test               全テスト実行
make test-backend       Go ユニットテスト
make test-frontend      Vitest フロントエンドテスト
make lint               全リンター実行
make lint-go            Go リンター (golangci-lint)
make lint-js            JS/TS リンター (ESLint)
make lint-css           CSS リンター (Stylelint)
make fmt                Go・テンプレートフォーマット
make tidy               go mod tidy
make watch              全体ウォッチ & リビルド
make watch-backend      バックエンドホットリロード (air)
make watch-frontend     フロントエンドウォッチ
make generate-swagger   Swagger API仕様生成
```

### 5.2 Webpack ビルドパイプライン

- **設定**: `webpack.config.ts`
- **エントリーポイント**:
  - `web_src/js/index.ts` → `public/assets/js/index.js`
  - テーマCSS → 個別CSSバンドル
  - Swagger, iframe等の standalone エントリー
- **最適化**: esbuild ミニファイ、CSS分離、コードスプリッティング、遅延ロード

### 5.3 設定ファイル一覧

| ファイル | 用途 |
|---------|------|
| `webpack.config.ts` | Webpack設定 |
| `tailwind.config.ts` | Tailwind CSS設定 |
| `tsconfig.json` | TypeScript設定 |
| `eslint.config.ts` | ESLint設定 |
| `stylelint.config.js` | CSS Lint設定 |
| `vitest.config.ts` | フロントエンドテスト設定 |
| `playwright.config.ts` | E2Eテスト設定 |
| `.air.toml` | ホットリロード設定 |

---

## 6. 設定システム

- **形式**: INI (`gopkg.in/ini.v1`)
- **デフォルトパス**: `custom/conf/app.ini`
- **読込**: `modules/setting/setting.go` の `InitCfgProvider()` → `LoadSettings()`
- **パス解決優先順位**:
  1. CLI フラグ (`--config`, `--work-path`, `--custom-path`)
  2. 環境変数 (`$GITEA_WORK_DIR`, `$GITEA_CUSTOM`)
  3. バイナリ配置ディレクトリからの相対パス

### 主要設定セクション

| セクション | 内容 |
|-----------|------|
| `[server]` | HTTP/HTTPS、ドメイン、ポート (3000)、SSL/TLS |
| `[database]` | DB接続、種別、AutoMigration |
| `[cache]` | キャッシュバックエンド (Redis, Memcache, memory) |
| `[session]` | セッションストレージ |
| `[service]` | 機能トグル、登録、OAuth |
| `[mail]` | SMTP 設定 |
| `[webhook]` | Webhook 設定 |
| `[cors]` | CORS 設定 |

---

## 7. アーキテクチャ上の設計パターン

1. **レイヤードアーキテクチャ**: Models → Services → Routers → Templates の明確な層分離
2. **ドメイン分割**: 各層内でドメインごとにサブパッケージ化 (repo, issue, user, org 等)
3. **ミドルウェアチェーン**: chi ルーターのミドルウェアで横断的関心事を処理
4. **コンテキストベースのDB操作**: トランザクションはコンテキスト経由で管理
5. **サーバーサイドレンダリング + クライアント強化**: Go テンプレートでHTML生成、Vue/HTMX で動的に強化
6. **カスタムオーバーライド**: `custom/` ディレクトリでテンプレート・設定のユーザーカスタマイズが可能
7. **マルチDB対応**: xorm の抽象化で4種のDBをサポート
8. **グレースフルシャットダウン**: `modules/graceful/` でシグナルハンドリング
