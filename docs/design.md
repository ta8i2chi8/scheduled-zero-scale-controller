# ScheduledZeroScale 設計

仕様は [spec.md](./spec.md) を参照。

## フレームワーク

kubebuilder（controller-runtime ベース）を使う。ひな形は kubebuilder で生成し、生成物の構成と Makefile（`make manifests` / `make test` / `make run`）にそのまま従う。

### ひな形の作り方

使ったバージョン: Go 1.27.3（asdf）、kubebuilder v4.16.0（Homebrew）。生成された `go.mod` は `go 1.26.0`、controller-runtime v0.25.0、Kubernetes 1.37。

前提: asdf（golang プラグイン）と Homebrew が入っていること。

```sh
# 1. Go をプロジェクト単位で固定する（プロジェクト直下に .tool-versions ができる）
asdf install golang 1.27.3
asdf set golang 1.27.3
go version                 # go1.27.3 になっていること

# 2. kubebuilder を入れる（依存で brew の go も入るが、PATH は asdf の shims が先なので影響しない）
brew install kubebuilder
kubebuilder version
which go                   # ~/.asdf/shims/go であること

# 3. プロジェクトの初期化（go.mod・Makefile・cmd/main.go・config/ などができる）
kubebuilder init --domain example.com --repo github.com/ta8i2chi8/scheduled-zero-scale-controller

# 4. API（CRD の型）とコントローラーのひな形を作る
#    api/v1alpha1/scheduledzeroscale_types.go と internal/controller/ ができる
kubebuilder create api --group sre --version v1alpha1 --kind ScheduledZeroScale --resource --controller

# 5. 実装で使う依存を追加する
go get github.com/robfig/cron/v3 k8s.io/utils

# 6. 型・マーカーを書いたら CRD / RBAC / DeepCopy を再生成する
make manifests generate

# 7. テスト（初回は setup-envtest と envtest 用バイナリが ./bin に落ちる）
make test
```

`make lint` の注意: kubebuilder v4.16.0 が固定する golangci-lint v2.13.1 は Go 1.27 に未対応（`export data version 5 is greater than maximum supported version 4`）。対応版が出るまでは Go 1.26 でビルドして実行する。

```sh
rm -f bin/golangci-lint bin/golangci-lint-*
ASDF_GOLANG_VERSION=1.26.4 make lint
```

### マーカー

- CRD のバリデーション（必須項目など）は `+kubebuilder:validation:*` マーカーで記述する。
- RBAC は `+kubebuilder:rbac:*` マーカーで記述する。

## 構成

| パス | 役割 |
|---|---|
| `api/v1alpha1/scheduledzeroscale_types.go` | CRD の型定義（kubebuilder 生成） |
| `internal/schedule` | cron（`github.com/robfig/cron/v3`）による状態判定の純粋関数。現在時刻は引数で受け取る |
| `internal/controller/scheduledzeroscale_controller.go` | Reconciler。時刻は `k8s.io/utils/clock` の `Clock` を注入してテスト可能にする |

## Watch

- `ScheduledZeroScale`（主）
- `Deployment`（同じ Namespace の ScheduledZeroScale へマッピングする）

## RBAC

- `deployments`: get / list / watch / patch
- `events`（`events.k8s.io`）: create / patch（controller-runtime v0.25 の新しい Events API を使うため）

## その他

- コンテナに tzdata を同梱する（`import _ "time/tzdata"`）。

## 社内 dev クラスタへの導入時（参考）

- 対象 Deployment の Git マニフェスト／values から `replicas` を消し、ラベルを追加する。Helm チャートが `replicas` を必ず出力しないか確認する。
- 既存 Deployment から `replicas` を消すと 1 台に落ちる問題に注意する（事前に last-applied から外す、または先にコントローラーに所有させる）。
- コントローラーが付けるアノテーションは Git に無いフィールドなので、ArgoCD の差分にはならない。
- 反映は PR 経由で人間が行う。

## 検証

### `internal/schedule` の表駆動テスト

- 平日昼・平日夜・週末
- 境界時刻ちょうど
- 7 日間発火なし
- タイムゾーン違い

### envtest（偽の時計で時刻を進める）

- replicas 3 → スケールインで 0＋アノテーション "3" → 元に戻して 3＋アノテーション削除
- スケールイン済の間に手動で増やしても 0 に戻る／アノテーションは上書きされない
- 通常の時間帯はアノテーションの無い Deployment に触らない
- suspend 中は変わらない
- アノテーション不正・複数一致の status
- finalizer による復帰

### kind

- `schedule.start: "*/4 * * * *"` / `schedule.end: "2-59/4 * * * *"` のような短い周期で、実際にスケールインと復帰を確認する。
- `kubectl get deploy -o yaml` のアノテーション、`kubectl get scheduledzeroscale -o yaml`、Events を確認する。
