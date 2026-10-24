# ScheduledZeroScale 仕様

## 目的

dev クラスタの Deployment を、夜間・休日など決まった時間帯に 0 台へスケールインし、時間帯の終了時に元の台数へ戻すことでコストを下げる。

## 前提

- ArgoCD の selfHeal と衝突しないよう、対象 Deployment の Git マニフェストには `replicas` を書かない。
- 元の台数は、スケールイン時にコントローラーが Deployment のアノテーションへ記録し、元に戻した後に削除する（Git には書かない）。
- 台数（replicas）の増減は水平スケールなので scale in / scale out と呼ぶ（scale up / down は CPU・メモリなどの垂直スケールを指すため使わない）。

## API

- Group/Version/Kind: `sre.example.com/v1alpha1` `ScheduledZeroScale`（group 名は仮）
- スコープ: Namespaced
- 対象: CR と同じ Namespace の Deployment のみ

```yaml
apiVersion: sre.example.com/v1alpha1
kind: ScheduledZeroScale
metadata:
  name: office-hours
  namespace: app-dev
spec:
  selector:                 # 必須。metav1.LabelSelector
    matchLabels:
      scheduled-zero-scale: office-hours
  timeZone: Asia/Tokyo      # 必須。IANA 名
  schedule:                 # 必須
    start: "0 21 * * 1-5"   # 必須。5 フィールド cron。0 台にする時刻
    end:   "0 8 * * 1-5"    # 必須。5 フィールド cron。元の台数に戻す時刻
                            # @daily などの記述子や CRON_TZ= 付き（timeZone を上書きしてしまう）は不正扱い
  suspend: false            # 任意。true の間は一切スケールしない
status:
  observedGeneration: 3
  scaleState: ScaledIn      # Normal（通常＝end 後）| ScaledIn（スケールイン済＝start 後）
  lastTransitionTime: "2026-10-23T21:00:00+09:00"  # 直近に発火した cron の時刻
  nextTransitionTime: "2026-10-24T08:00:00+09:00"
  targets:                  # 一致した Deployment と結果
    - name: api
      result: ScaledIn      # ScaledIn | Restored | Unchanged | InvalidAnnotation | Conflict
  conditions:               # Ready / Suspended
    - type: Ready
      status: "True"
      reason: Reconciled
```

### Deployment 側

Git で管理する。`replicas` は書かず、ラベルのみ付ける。

```yaml
kind: Deployment
metadata:
  labels:
    scheduled-zero-scale: office-hours
```

### コントローラーが付けるアノテーション

スケールイン済の間だけ付く。Git には書かない。

```yaml
metadata:
  annotations:
    example.com/replicas-before-scale-in: "3"
```

## 振る舞い

### 1. 現在あるべき状態の判定

「`start` と `end` のうち、**最後に来たのはどちらか**」で決める。

| 最後に来たもの | 状態 |
|---|---|
| `start`（0 台にする時刻） | `ScaledIn` |
| `end`（元に戻す時刻） | `Normal` |

#### 例

`start: "0 21 * * 1-5"`（平日 21:00）/ `end: "0 8 * * 1-5"`（平日 8:00）、`timeZone: Asia/Tokyo` の場合:

| 現在時刻 | 最後の start | 最後の end | 状態 |
|---|---|---|---|
| 水曜 12:00 | 火曜 21:00 | 水曜 8:00 | `Normal` |
| 水曜 23:00 | 水曜 21:00 | 水曜 8:00 | `ScaledIn` |
| 土曜 15:00 | 金曜 21:00 | 金曜 8:00 | `ScaledIn`（週末はずっとスケールイン済） |
| 月曜 9:00 | 金曜 21:00 | 月曜 8:00 | `Normal` |

#### 補足

- 時刻は `timeZone` で解釈する。
- `start` と `end` が同じ時刻に発火した場合は `Normal` とする（判断できないときは起こしておく）。
- 「最後に来た時刻」は現在から過去 7 日の範囲で探す。7 日以内にどちらも来ていなければ `Normal` とする（判断できないときは起こしておく）。
- 「何時にイベントが発火したか」ではなく「今どうあるべきか」を毎回計算するため、コントローラーが止まっていて切り替え時刻を逃しても、再開時に正しい状態へ戻せる。
- 7 日はスケジュール判定の探索範囲であり、コントローラーの停止時間の上限ではない。平日のスケジュールなら、何日停止していても再開時に正しく判定できる。7 日以内に一度も来ないスケジュール（例: 月 1 回）は判定できず `Normal` になる。

#### 例: スケールイン済の間にコントローラーが停止した場合

水曜 21:00 にスケールイン（`replicas: 0`、アノテーション `"3"`）した後、コントローラーが停止したとする。停止中は Deployment がそのまま残る（Git に `replicas` が無いので ArgoCD も戻さない）。再開時に全 ScheduledZeroScale が Reconcile され、次のようになる。

| 再開時刻 | 判定 | 結果 |
|---|---|---|
| 木曜 10:00（通常の時間帯） | `Normal` | アノテーションの値 3 に戻し、アノテーションを削除する。本来の 8:00 より遅れて戻る |
| 木曜 22:00（次のスケールイン時間帯） | `ScaledIn` | アノテーションがあるので上書きせず 0 のまま。金曜 8:00 に 3 へ戻る |
| 停止中に誰かが手で 5 台に変えていた | `Normal` | アノテーションの値 3 で上書きする（手で変えた 5 は残らない） |

### 2. ScaledIn のとき

| Deployment の状態 | 動作 |
|---|---|
| アノテーションなし | 現在の `replicas` をアノテーションに記録し、`replicas: 0` にする。2 つは 1 回のパッチでまとめて適用する（途中で落ちても「台数を記録せずに 0 にした」状態にならない） |
| アノテーションあり | スケールイン済とみなし、アノテーションは上書きしない。`replicas` が 0 でなければ 0 に戻す（手動で増やされても戻す） |

### 3. Normal のとき

| Deployment の状態 | 動作 |
|---|---|
| アノテーションあり | `replicas` をアノテーションの値に戻し、アノテーションを削除する（1 回のパッチ） |
| アノテーションなし | 何もしない（通常時の台数は人や他の仕組みに任せる） |

### 4. 書き込み方式

- Server-Side Apply、fieldManager `scheduledzeroscale-controller`。
- コントローラーが所有するのは `spec.replicas` と上記アノテーションだけ。
- 元に戻すときはアノテーションを含めずに apply することで削除する。
- 差分がなければ書かない。
- 適用したら Event を記録する（`events.k8s.io` の Events API）。

### 5. 次回の起床

- `RequeueAfter = min(次回の start, 次回の end) - 現在時刻`
- どちらも二度と発火しない cron（例: `0 0 30 2 *`）なら再実行を予約しない（`nextTransitionTime` は空）。
- Deployment の作成・変更も監視し、スケールイン済の時間帯に新しく作られた Deployment もスケールインする。

### 6. suspend

- `suspend: true` の間は判定も適用もせず、status に `Suspended=True` を出す。
- 夜間に使いたいときは suspend にしてから手で `kubectl scale` する。
- アノテーションは残るので、suspend を外したのが通常の時間帯ならアノテーションの台数に戻る。

### 7. 異常系

| 事象 | 動作 |
|---|---|
| cron / timeZone が不正 | `Ready=False`（reason `InvalidSchedule`）。再試行しない（spec が直るまで待つ） |
| selector が不正 | `Ready=False`（reason `InvalidSelector`）。再試行しない（spec が直るまで待つ） |
| アノテーションの値が不正（0 以上の整数でない） | その Deployment はスキップし `InvalidAnnotation` を記録 |
| 1 つの Deployment に複数の ScheduledZeroScale が一致 | その Deployment はスキップし `Conflict` を記録 |

### 8. CR 削除時

finalizer で、一致している Deployment のうちアノテーションがあるものを元の台数に戻してから削除する。

## v1 の対象外

- 祝日
- StatefulSet
- Argo Rollouts の `Rollout`
- スケールイン済の間にラベルを外された Deployment の自動復帰（アノテーションは残るので、手で戻す）
