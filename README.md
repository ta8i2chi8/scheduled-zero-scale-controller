# scheduled-zero-scale-controller

dev クラスタの Deployment を、夜間・休日など決まった時間帯に 0 台へスケールインし、時間帯の終了時に元の台数へ戻すことでコストを下げる Kubernetes コントローラー。

## 概要

`ScheduledZeroScale` カスタムリソースで、対象 Deployment（ラベルで選択）とスケジュール（cron とタイムゾーン）を指定する。

```yaml
apiVersion: sre.example.com/v1alpha1
kind: ScheduledZeroScale
metadata:
  name: office-hours
  namespace: app-dev
spec:
  selector:
    matchLabels:
      scheduled-zero-scale: office-hours
  timeZone: Asia/Tokyo
  schedule:
    start: "0 21 * * 1-5"   # 0 台にする時刻
    end:   "0 8 * * 1-5"    # 元の台数に戻す時刻
```

- スケールイン時に元の台数を Deployment のアノテーション（`example.com/replicas-before-scale-in`）へ記録し、元に戻した後に削除する。
- 「start と end のどちらが最後に来たか」で今あるべき状態を毎回計算するため、コントローラーが止まっていても再開時に正しい状態へ戻せる。
- 対象 Deployment の Git マニフェストには `replicas` を書かない（ArgoCD の selfHeal と衝突させないため）。

詳しくは [仕様書](docs/spec.md) と [設計書](docs/design.md) を参照。

## はじめに

### 前提

- Go v1.26 以上（このリポジトリでは asdf の `.tool-versions` で 1.27.3 に固定）
- docker 17.03 以上
- kubectl v1.11.3 以上
- Kubernetes v1.11.3 以上のクラスタへのアクセス

### クラスタへのデプロイ

**`IMG` で指定した場所へイメージをビルドして push する:**

```sh
make docker-build docker-push IMG=<some-registry>/scheduled-zero-scale-controller:tag
```

**注意:** イメージは指定したレジストリに公開され、実行環境から pull できる必要がある。上のコマンドが失敗する場合は、レジストリへの権限を確認すること。

**CRD をクラスタにインストールする:**

```sh
make install
```

**`IMG` で指定したイメージでコントローラーをデプロイする:**

```sh
make deploy IMG=<some-registry>/scheduled-zero-scale-controller:tag
```

> **注意**: RBAC のエラーが出る場合は、cluster-admin 権限を付与するか、管理者としてログインする必要がある。

**リソースを作成する**

`config/samples` のサンプルを適用できる:

```sh
kubectl apply -k config/samples/
```

> **注意**: サンプルのラベル（`scheduled-zero-scale: office-hours`）を付けた Deployment が同じ Namespace に必要。

### アンインストール

**クラスタからリソース（CR）を削除する:**

```sh
kubectl delete -k config/samples/
```

**クラスタから API（CRD）を削除する:**

```sh
make uninstall
```

**クラスタからコントローラーを削除する:**

```sh
make undeploy
```

## 配布

利用者への配布方法は次のとおり。

### すべての YAML をまとめたバンドルを配布する

1. レジストリに公開したイメージ用のインストーラーをビルドする:

```sh
make build-installer IMG=<some-registry>/scheduled-zero-scale-controller:tag
```

**注意:** 上の make ターゲットは `dist` ディレクトリに `install.yaml` を生成する。このファイルには、依存なしでこのプロジェクトをインストールするのに必要な、Kustomize でビルドしたすべてのリソースが含まれる。

2. インストーラーを使う

利用者は `kubectl apply -f <YAML バンドルの URL>` でインストールできる。例:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/scheduled-zero-scale-controller/<tag or branch>/dist/install.yaml
```

### Helm チャートを配布する

1. helm プラグイン（任意）でチャートを生成する

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. `dist/chart` にチャートが生成されるので、利用者はそこから取得できる。

**注意:** プロジェクトを変更した場合は、同じコマンドでチャートを更新して最新の変更を反映する。また、webhook を作成した場合は `--force` を付けて実行し、`dist/chart/values.yaml` や `dist/chart/manager/manager.yaml` に加えていた独自の設定を手で入れ直す必要がある。

## 開発

- テスト: `make test`（`internal/schedule` の単体テストと envtest）
- lint: `make lint`（Go 1.27 での注意点は [設計書](docs/design.md) の「ひな形の作り方」を参照）

**注意:** `make help` で利用できる `make` ターゲットの一覧を確認できる。

詳しくは [Kubebuilder のドキュメント](https://book.kubebuilder.io/introduction.html) を参照。

## ライセンス

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
