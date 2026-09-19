# Knowledge Base

## Context

`flac_analyzer_forwin` の RAM/VRAM 配置、中央Admission、CPU/GPU wavefront、PostgreSQL解析分岐ロードマップを 2026-09-13 に中断した時点の検証済み知識。

## Findings

- Phase 1〜3 はローカル実装・静的検証済み。実機RTX負荷・長時間ベンチはユーザー側検証境界。
- Phase 4の `AnalysisDecision` は `FullAnalysis | MixOnly | StemsOnly | Skip`。完全行だけSkipし、旧schema、不正JSON、`analyzed_at`欠落はFullへ倒す。
- PostgreSQL現行schemaには正規化 `(filepath, track_number)` の一意制約とversion列がない。
- ユーザーは、正規化キーとversion列を追加するmigration方式を採用したが、migration実装前に中断した。
- 必須計画監査はサブエージェントが権威ファイル位置を誤認し、3回とも計画内容を監査できず終了した。

## Morphism

`CUE展開済みTask → 正規化pairの一括DB照合 → AnalysisDecision → decision別資源Lease/Stage → version条件付きingest`。DB障害・JSON不正・version競合は未登録やSkipへ丸めず、明示失敗またはretryへ送る。

