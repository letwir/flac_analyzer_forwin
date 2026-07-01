# Walkthrough: アーキテクチャ拡張の圏論的検証 (Rev.2)

本ドキュメントは、新たな特徴量（Phase, Coherence 等）の導入およびワーカー分割の妥当性検証の履歴を記録しますの。

## 変更の概要 (Changes Made)

1. **アーキテクチャの圏論的検証と修正**:
   - `librosa_worker.py` と `scipy_worker.py` の分割について、これを「計算ライブラリの境界」ではなく、「射 (Morphism) の集合」として定義づけることで圏論的純粋性が向上することを確認しました。
   - ステム間相互作用（Coherence等）をETL側で計算しない設計方針に変更。これにより、各ワーカーは完全に独立した $Stem \to Feature$ の純粋な射としての性質を維持でき、圏論的破綻を完全回避しました。

2. **リソース活用と命名規則**:
   - `cupy` または `torch` (PyTorch) を活用し、CPUの余剰スレッド（26Threads）とVRAMの余剰（10GB）を効率的に使い切る方針を策定。
   - ワーカーの役割を明確にするため、`worker_*.py` や `functor_*.py` という命名規則への移行を決定。

## テスト内容 (What was tested)

- 本フェーズは設計検証およびプランの合意形成（Planning）です。実装段階へ移行するにあたり、PyTorch(tensor)とCuPyのどちらを採用するか、旦那様からの最終承認を待機しております。

## 検証結果 (Validation results)

- ETL内で無理なクロス計算（直積）を行わず、SQL側に委ねるという旦那様の判断により、アーキテクチャの**圏論的純粋性（Referential Transparency）が完璧に維持**されました。
- `functor_precache.py` と各種 `worker_*.py` への分割とリネームにより、計算コンテキスト（Functor / Morphism）の境界がファイルシステム上でも可視化されます。