# TASK (MIDDLE RANGE)

## 中期アーキテクチャ最適化計画 (Mid-term Architecture Plan)
本計画は、大容量FLAC+CUE（約600MB）の解析において、Win11/64GB RAM/RTX3060環境下での致命的なOOM（Out of Memory）を回避し、システムの圏論的整合性を完全なものにするための中期的なリファクタリング方針です。
1. **メモリフットプリントとOOM(Out Of Memory)の完全制圧**
   - **課題**: 現在の最大並列数(`workers=8`)でDemucsが走ると、`audio` と `stem_ctx` の多重展開により最大で約168GBのRAMを要求し、即時クラッシュします。
   - **対策**: `psutil` を用いた動的Backpressure（流量制限）を実装し、空きRAM（55GB想定）やVRAM（12GB）が閾値を下回った場合はプロセスを待機・抑制します。また、DSP Pre-warming時の全中間配列一斉展開や、共有メモリとコンシューマRAM間の波形データ二重存在を解消し、Peak RAMを劇的に削減します。
2. **FLACプレウォーミング戦略の刷新**
   - **課題**: `float32` でのPCM全展開は、1時間あたり数GBのRAMを消費し、メモリ圧迫の元凶となっています。
   - **対策**: FLACの「圧縮バイナリ（数百MB）」自体をRAMに常駐させ、必要な曲区間のみを `io.BytesIO` 経由でオンデマンドデコード（あるいは厳密なZero-copy Viewの管理）するアプローチへ移行し、メモリ効率を最大化します。
3. **圏論的整合性（Category Theory Soundness）の回復**
   - **MD5の純粋性確保**: RAMに置いたFLAC波形バイナリからCUEに沿って切り出して計測。flacヘッダには曲ごとのMD5は存在しない。
   - **射の一意性（Morphism Uniqueness）の統合**: `process_single_flac_file` と `run_producer` に分断されたCUE処理およびファイル解析のロジック（「File → DB」の射の二重定義）を統合します。
   - **Endofunctorの純粋化**: `AudioContext` の遅延キャッシュ（Mutating状態）による副作用とマルチプロセスの競合リスクを排除し、純粋な値オブジェクトとして再設計します。
4. **OS互換性の担保**
   - **課題**: `save_stems_to_shm` 等でハードコードされている `/dev/shm` はWindows環境に存在しません。
   - **対策**: Python 3.8+ の `multiprocessing.shared_memory` またはOS非依存の一時ディレクトリマッピングへ完全移行し、Windows 11環境でのネイティブ動作を保証します。
   - Linuxでも動くようにOS互換性を最大担保する。圏論的破綻がある場合は圏論を優先する。
